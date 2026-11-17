package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestVoiceTranscodesWAVToMP3(t *testing.T) {
	for _, rate := range []int{8000, 11025, 12000, 16000, 22050, 24000, 32000, 44100, 48000} {
		wav := demoToneWAV(1.3, rate)
		audio, err := decodeWAV(bytes.NewReader(wav))
		if err != nil {
			t.Fatal(err)
		}
		if d := audio.DurationMS(); d < 1250 || d > 1350 {
			t.Fatalf("%d Hz: unexpected duration %d", rate, d)
		}
		encoded, err := encodeMP3(audio)
		if err != nil {
			t.Fatalf("%d Hz: %v", rate, err)
		}
		frames, frameRate, frameSamples, err := walkMP3Frames(encoded)
		if err != nil {
			t.Fatalf("%d Hz: malformed mp3: %v", rate, err)
		}
		if frameRate != 24000 {
			t.Fatalf("%d Hz: voice must be encoded at 24 kHz, got %d", rate, frameRate)
		}
		durationMS := int64(frames*frameSamples) * 1000 / int64(frameRate)
		if durationMS < 1300 || durationMS > 1300+int64(frameSamples)*1000/int64(frameRate)+5 {
			t.Fatalf("%d Hz: decoded duration %d ms does not match 1300 ms", rate, durationMS)
		}
	}
	stereo := pcmAudio{Samples: make([]int16, 4800), SampleRate: 48000}
	if res := resampleLinear(stereo, 24000); len(res.Samples) != 2400 || res.SampleRate != 24000 {
		t.Fatalf("resampling must keep the duration: %d samples at %d", len(res.Samples), res.SampleRate)
	}
	ramp := pcmAudio{Samples: []int16{0, 100, 200, 300}, SampleRate: 16000}
	if res := resampleLinear(ramp, 24000); len(res.Samples) != 6 || res.Samples[1] != 67 || res.Samples[3] != 200 {
		t.Fatalf("linear interpolation expected, got %v", res.Samples)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "v.wav")
	os.WriteFile(path, demoToneWAV(2, 48000), 0o600)
	file, err := prepareVoice(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !file.Voice || file.Mime != "audio/mpeg" || file.Kind != "audio" || len(file.Waveform) != voiceWaveformBars || file.DurationMS < 1900 || file.DurationMS > 2100 {
		t.Fatalf("unexpected voice file %+v", file)
	}
	peak := 0.0
	for _, v := range file.Waveform {
		if v < 0 || v > 1 {
			t.Fatalf("waveform out of range %v", file.Waveform)
		}
		peak = max(peak, v)
	}
	if peak != 1 {
		t.Fatalf("waveform must be normalised to its peak, got %v", peak)
	}
	if got := waveformBytes([]float64{0, 0.5, 1}); !bytes.Equal(got, []byte{0, 50, 100}) {
		t.Fatalf("unexpected waveform bytes %v", got)
	}
	for name, data := range map[string][]byte{
		"not wav":  []byte("hello world, not a wav file"),
		"8 bit":    patchWAV(demoToneWAV(0.5, 16000), 34, 8),
		"bad rate": patchWAV32(demoToneWAV(0.5, 16000), 24, 12345),
	} {
		p := filepath.Join(dir, name+".wav")
		os.WriteFile(p, data, 0o600)
		if _, err := prepareVoice(p, 0); toErrorBody(err).Code != CodeBadRequest {
			t.Fatalf("%s: expected bad_request, got %v", name, err)
		}
	}
}

func patchWAV(data []byte, offset int, value uint16) []byte {
	out := bytes.Clone(data)
	out[offset], out[offset+1] = byte(value), byte(value>>8)
	return out
}

func patchWAV32(data []byte, offset int, value uint32) []byte {
	out := bytes.Clone(data)
	out[offset], out[offset+1], out[offset+2], out[offset+3] = byte(value), byte(value>>8), byte(value>>16), byte(value>>24)
	return out
}

var mp3Bitrates = map[bool][]int{
	true:  {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320},
	false: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},
}

var mp3Rates = map[int][]int{3: {44100, 48000, 32000}, 2: {22050, 24000, 16000}, 0: {11025, 12000, 8000}}

func walkMP3Frames(data []byte) (frames, rate, samples int, err error) {
	pos := 0
	for pos < len(data) {
		if len(data)-pos < 4 || data[pos] != 0xFF || data[pos+1]&0xE0 != 0xE0 {
			return frames, rate, samples, fmt.Errorf("no frame sync at byte %d of %d (frame %d)", pos, len(data), frames)
		}
		version := int(data[pos+1]>>3) & 3
		layer := int(data[pos+1]>>1) & 3
		bitrateIdx := int(data[pos+2] >> 4)
		rateIdx := int(data[pos+2]>>2) & 3
		padding := int(data[pos+2]>>1) & 1
		rates, ok := mp3Rates[version]
		if !ok || layer != 1 || rateIdx > 2 || bitrateIdx == 0 || bitrateIdx == 15 {
			return frames, rate, samples, fmt.Errorf("invalid header at byte %d", pos)
		}
		mpeg1 := version == 3
		r := rates[rateIdx]
		if rate != 0 && r != rate {
			return frames, rate, samples, fmt.Errorf("sample rate changes at frame %d", frames)
		}
		rate = r
		bitrate := mp3Bitrates[mpeg1][bitrateIdx] * 1000
		size := 72*bitrate/r + padding
		samples = 576
		if mpeg1 {
			size = 144*bitrate/r + padding
			samples = 1152
		}
		if pos+size > len(data) {
			return frames, rate, samples, fmt.Errorf("frame %d truncated: %d bytes left, %d needed", frames, len(data)-pos, size)
		}
		pos += size
		frames++
	}
	if frames == 0 {
		return 0, 0, 0, fmt.Errorf("no frames")
	}
	return frames, rate, samples, nil
}
