package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"

	"github.com/pion/webrtc/v4"
)

var (
	peerConnections = make(map[string]*webrtc.PeerConnection)
	mu              sync.Mutex
	connCounter     int
)

func main() {
	// 捕获 Ctrl+C 信号，关闭所有连接
	closed := make(chan os.Signal, 1)
	signal.Notify(closed, os.Interrupt)
	go func() {
		<-closed
		fmt.Println("\nShutting down SFU...")
		mu.Lock()
		for _, pc := range peerConnections {
			pc.Close()
		}
		mu.Unlock()
		os.Exit(0)
	}()

	startSFU()
}

func startSFU() {
	// 使用 bufio 读取整行输入
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Println("\n等待新的 SDP (请输入 Base64 编码的 SDP):")
		input, err := reader.ReadString('\n')
		if err != nil {
			fmt.Println("读取输入失败:", err)
			continue
		}
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}

		offer := webrtc.SessionDescription{}
		if err := decode(input, &offer); err != nil {
			fmt.Println("SDP 解码失败:", err)
			continue
		}

		// 处理新的 WebRTC 连接
		answer := handleNewConnection(&offer)
		fmt.Println("返回的 SDP Answer (Base64 编码):")
		fmt.Println(encode(answer))
	}
}

func handleNewConnection(offer *webrtc.SessionDescription) *webrtc.SessionDescription {
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	}

	peerConnection, err := webrtc.NewPeerConnection(config)
	if err != nil {
		panic(err)
	}

	// 生成唯一连接 ID，并存储连接
	mu.Lock()
	connCounter++
	connID := fmt.Sprintf("conn-%d", connCounter)
	peerConnections[connID] = peerConnection
	mu.Unlock()

	// 当收到远程 Track 时，创建本地 Track 并转发给其他所有连接
	peerConnection.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		fmt.Printf("收到来自 %s 的远程流: %s\n", connID, track.Kind().String())

		localTrack, err := webrtc.NewTrackLocalStaticRTP(track.Codec().RTPCodecCapability, track.ID(), track.StreamID())
		if err != nil {
			fmt.Println("创建本地 Track 失败:", err)
			return
		}

		// 将本地 Track 添加到所有其他连接
		mu.Lock()
		for id, pc := range peerConnections {
			if id == connID {
				continue
			}
			rtpSender, err := pc.AddTrack(localTrack)
			if err != nil {
				fmt.Printf("在连接 %s 添加 Track 失败: %v\n", id, err)
				continue
			}
			// 持续读取 RTCP 数据包
			go func() {
				rtcpBuf := make([]byte, 1500)
				for {
					if _, _, err := rtpSender.Read(rtcpBuf); err != nil {
						return
					}
				}
			}()
		}
		mu.Unlock()

		// 持续读取 RTP 包，并写入到本地 Track，实现转发
		go func() {
			for {
				rtpPacket, _, err := track.ReadRTP()
				if err != nil {
					return
				}
				if err := localTrack.WriteRTP(rtpPacket); err != nil {
					return
				}
			}
		}()
	})

	// 监听 ICE 状态变化，断线时清理连接
	peerConnection.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		fmt.Printf("连接 %s ICE 状态变更: %s\n", connID, state.String())
		if state == webrtc.ICEConnectionStateDisconnected || state == webrtc.ICEConnectionStateFailed {
			mu.Lock()
			delete(peerConnections, connID)
			mu.Unlock()
			fmt.Printf("连接 %s 已删除\n", connID)
		}
	})

	// 设置远程描述，生成 Answer，并等待 ICE 收集完成
	if err := peerConnection.SetRemoteDescription(*offer); err != nil {
		panic(err)
	}

	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		panic(err)
	}

	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)
	if err := peerConnection.SetLocalDescription(answer); err != nil {
		panic(err)
	}
	<-gatherComplete

	return peerConnection.LocalDescription()
}

// encode 将 SessionDescription 编码为 Base64
func encode(obj *webrtc.SessionDescription) string {
	b, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// decode 将 Base64 编码的字符串解码为 SessionDescription
func decode(in string, obj *webrtc.SessionDescription) error {
	b, err := base64.StdEncoding.DecodeString(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, obj)
}
