Tested https://github.com/Yeachan-Heo/oh-my-claudecode

Installed this plugin to Claude & executed two `/deep-interview`
- made batch Chinese speech-to-text
- based on this engine, made server-client architecture with recognition of streaming 'China international radio' - https://sk.cri.cn/905.m3u8

Server requires:
- ffmpeg
- sherpa-onnx-bin
- model 'sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17'

Finally, made Dockerfile

---

What I've learned:
- HLS (HTTP Live Streaming) — streaming protocol (created by Apple). Stream (audio/video) divided on short parts (in our example: 3 seconds) and return via usual HTTP (not using WebRTC/RTMP/...)
- .m3u8 - text file with playlist. Example of our m3u8 file:

```
#EXTM3U
#EXT-X-VERSION:3
#EXT-X-ALLOW-CACHE:NO
#EXT-X-MEDIA-SEQUENCE:1780260180
#EXT-X-TARGETDURATION:4
#EXTINF:3.008,
905-1780260180.ts?txspiseq=107248004929753481221
#EXTINF:3.008,
905-1780260181.ts?txspiseq=107248004929753481221
#EXTINF:3.008,
905-1780260182.ts?txspiseq=107248004929753481221
```
- onnx - universal, framework-agnostic, neural network format (layers, links, weights)
- sherpa-onnx - it's program, which can run inferring on this neural network. Accepts wav file as input
- ASR - Automatic Speech Recognition
