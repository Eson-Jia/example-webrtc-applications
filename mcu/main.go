package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

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
	//first read from 1.sdp
	sdp1, err := os.OpenFile("1.sdp", os.O_RDONLY, 0666)
	if err != nil {
		fmt.Println("failed to open 1.sdp")
		return
	}
	defer sdp1.Close()
	reader := bufio.NewReader(sdp1)
	content, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		fmt.Println("failed to read 1.sdp")
		return
	}
	inputStr := strings.TrimSpace(content)
	if inputStr == "" {
		fmt.Println("failed to read input")
		return
	}
	offer := webrtc.SessionDescription{}
	if err := decode(inputStr, &offer); err != nil {
		fmt.Println("SDP 解码失败:", err)
		return
	}

	// 处理新的 WebRTC 连接
	answer := handleNewConnection(&offer)
	fmt.Println("返回的 SDP Answer (Base64 编码):")
	fmt.Println(encode(answer))
	time.Sleep(time.Second * 10)
	{
		//read from 2.sdp
		sdp2, err := os.OpenFile("2.sdp", os.O_RDONLY, 0666)
		if err != nil {
			fmt.Println("failed to open 2.sdp")
			return
		}
		defer sdp2.Close()
		reader := bufio.NewReader(sdp2)
		content, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			fmt.Println("failed to read 2.sdp")
			return
		}
		inputStr := strings.TrimSpace(content)
		if inputStr == "" {
			fmt.Println("failed to read input")
			return
		}
		offer := webrtc.SessionDescription{}
		if err := decode(inputStr, &offer); err != nil {
			fmt.Println("SDP 解码失败:", err)
			return
		}

		// 处理新的 WebRTC 连接
		answer := handleNewConnection(&offer)
		fmt.Println("返回的 SDP Answer (Base64 编码):")
		fmt.Println(encode(answer))
	}

	select {}
}

// Read incoming RTCP packets
// Before these packets are returned they are processed by interceptors. For things
// like NACK this needs to be called.
func rtcpReader(rtpSender *webrtc.RTPSender) {
	go func() {
		rtcpBuf := make([]byte, 1500)
		for {
			if _, _, rtcpErr := rtpSender.Read(rtcpBuf); rtcpErr != nil {
				return
			}
		}
	}()
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

	audioTrack, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "pion")
	if err != nil {
		panic(err)
	}

	// Handle RTCP, see rtcpReader for why
	rtpAudioSender, err := peerConnection.AddTrack(audioTrack)
	if err != nil {
		panic(err)
	}
	rtcpReader(rtpAudioSender)

	// Create a Video Track
	videoTrack, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", "pion")
	if err != nil {
		panic(err)
	}

	// Handle RTCP, see rtcpReader for why
	rtpVideoSender, err := peerConnection.AddTrack(videoTrack)
	if err != nil {
		panic(err)
	}
	rtcpReader(rtpVideoSender)

	// 生成唯一连接 ID，并存储连接
	mu.Lock()
	connCounter++
	connID := fmt.Sprintf("conn-%d", connCounter)
	peerConnections[connID] = peerConnection
	mu.Unlock()

	// 当收到远程 Track 时，创建本地 Track 并转发给其他所有连接
	peerConnection.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		fmt.Printf("收到来自 %s 的远程流: %s\n", connID, track.Kind().String())
		var sender *webrtc.RTPSender
		if track.Kind() == webrtc.RTPCodecTypeAudio {
			sender = rtpAudioSender
		} else if track.Kind() == webrtc.RTPCodecTypeVideo {
			sender = rtpVideoSender
		}
		// 将本地 Track 添加到所有其他连接
		mu.Lock()
		for id, pc := range peerConnections {
			if id == connID {
				continue
			}
			// 持续读取 RTCP 数据包
			go func() {
				rtcpBuf := make([]byte, 1500)
				for {
					if _, _, err := sender.Read(rtcpBuf); err != nil {
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
				//send to other connections
				mu.Lock()
				for id, pc := range peerConnections {
					if id == connID {
						continue
					}
					_, err = pc.WriteRTP(rtpPacket)
					if err != nil {
						fmt.Println("failed to write rtp packet")
					}
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
	// 移除所有空白字符（包括换行、回车、空格）
	in = strings.ReplaceAll(in, "\n", "")
	in = strings.ReplaceAll(in, "\r", "")
	in = strings.ReplaceAll(in, " ", "")
	in = strings.TrimSpace(in)
	b, err := base64.StdEncoding.DecodeString(in)
	if err != nil {
		return fmt.Errorf("解码 Base64 失败，请检查 SDP 是否为有效的 Base64 编码: %w", err)
	}
	return json.Unmarshal(b, obj)
}
