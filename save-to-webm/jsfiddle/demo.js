/* SPDX-License-Identifier: MIT */
/* eslint-env browser */

const pc = new RTCPeerConnection({
  iceServers: [
    { urls: 'stun:stun.l.google.com:19302' }
  ]
});

const log = msg => {
  const logsElement = document.getElementById('logs');
  logsElement.innerHTML += msg + '<br>';
};

// 根据 50% 的概率选择使用摄像头或屏幕共享
const mediaPromise = Math.random() > 0.5
    ? navigator.mediaDevices.getDisplayMedia({ video: true, audio: true })
    : navigator.mediaDevices.getUserMedia({ video: true, audio: true });

// 当收到远程流时，将其显示到 video2 元素上
pc.ontrack = event => {
  log("收到远程流");
  document.getElementById('video2').srcObject = event.streams[0];
};

async function init() {
  try {
    const stream = await mediaPromise;
    const video1 = document.getElementById('video1');
    video1.srcObject = stream;

    // 将流中的每个轨道添加到 RTCPeerConnection
    stream.getTracks().forEach(track => {
      pc.addTrack(track, stream);
    });

    // 创建 Offer，并设置本地描述
    const offer = await pc.createOffer();
    await pc.setLocalDescription(offer);
  } catch (err) {
    log(err);
  }
}
init();

pc.oniceconnectionstatechange = () => {
  log(`ICE 状态: ${pc.iceConnectionState}`);
};

pc.onicecandidate = event => {
  // 当候选地址收集完成时（candidate 为 null），输出 SDP 信息
  if (!event.candidate) {
    const localSDP = btoa(JSON.stringify(pc.localDescription));
    document.getElementById('localSessionDescription').value = localSDP;
  }
};

// 用于设置远端 SDP 的函数，触发后开始建立连接
window.startSession = () => {
  const remoteSDPEncoded = document.getElementById('remoteSessionDescription').value;
  if (!remoteSDPEncoded) {
    return alert('Session Description must not be empty');
  }
  try {
    const remoteSDP = JSON.parse(atob(remoteSDPEncoded));
    pc.setRemoteDescription(remoteSDP).catch(log);
  } catch (err) {
    alert(err);
  }
};
