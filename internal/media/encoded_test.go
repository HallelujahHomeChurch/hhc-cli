package media

import (
	"strings"
	"testing"
)

func TestEncodedPlaylistRejectsExternalOrAmbiguousReferences(t *testing.T) {
	valid := "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:30\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-INDEPENDENT-SEGMENTS\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:30,\nseg-000000.m4s\n#EXT-X-ENDLIST\n"
	if values, target, err := parseEncodedPlaylist([]byte(valid), 1); err != nil || len(values) != 1 || target != 30 {
		t.Fatal("valid generated playlist rejected")
	}
	for _, bad := range []string{
		strings.Replace(valid, "seg-000000.m4s", "https://example.invalid/media", 1),
		strings.Replace(valid, "seg-000000.m4s", "../seg-000000.m4s", 1),
		strings.Replace(valid, "seg-000000.m4s", "seg-000001.m4s", 1),
		strings.Replace(valid, "#EXTINF:30,", "#EXTINF:NaN,", 1),
		strings.Replace(valid, "#EXTINF:30,", "#EXTINF:30,\n#EXTINF:30,", 1),
		strings.Replace(valid, "#EXT-X-ENDLIST", "", 1),
		strings.Replace(valid, "init.mp4", "https://example.invalid/init", 1),
		strings.Replace(valid, "#EXTINF:30,", "#EXT-X-KEY:METHOD=AES-128,URI=\"key\"\n#EXTINF:30,", 1),
	} {
		if _, _, err := parseEncodedPlaylist([]byte(bad), 1); err == nil {
			t.Fatal("accepted unsafe playlist")
		}
	}
}

// Independent golden fixture from Asset's bounded fragment probe contract.
func TestEncodedProbeRejectsWrongCodecTimelineAndMissingKeyframe(t *testing.T) {
	const valid = `{"streams":[{"index":0,"codec_name":"h264","codec_type":"video","width":1280,"height":720,"pix_fmt":"yuv420p","sample_aspect_ratio":"1:1","r_frame_rate":"30/1","extradata":"\n00000000: 0164 001f ffe1 001a 6764 001f acd9 4050  .d......gd....@P\n"},{"index":1,"codec_name":"aac","codec_type":"audio","profile":"LC","sample_rate":"48000","channels":2}],"packets":[{"stream_index":0,"pts_time":"0.000000","duration_time":"0.033333","flags":"K_"},{"stream_index":1,"pts_time":"0.000000","duration_time":"0.021333","flags":"K_"},{"stream_index":0,"pts_time":"0.033333","duration_time":"0.033333","flags":"__"},{"stream_index":1,"pts_time":"0.021333","duration_time":"0.021333","flags":"K_"},{"stream_index":1,"pts_time":"0.042666","duration_time":"0.021333","flags":"K_"}]}`
	r := RecordingRendition{Name: "720p", Width: 1280, Height: 720, FrameRate: 30}
	if value, err := parseEncodedProbe([]byte(valid), r); err != nil || value.codecs != "avc1.64001f,mp4a.40.2" {
		t.Fatalf("golden probe: %+v %v", value, err)
	}
	for _, change := range [][2]string{{`"flags":"K_"`, `"flags":"__"`}, {`"codec_name":"h264"`, `"codec_name":"hevc"`}, {`"pts_time":"0.033333"`, `"pts_time":"0.133333"`}, {`"pts_time":"0.000000"`, `"pts_time":"NaN"`}, {`"profile":"LC"`, `"profile":"HE-AAC"`}, {`0164 001f`, `0264 001f`}} {
		if _, err := parseEncodedProbe([]byte(strings.Replace(valid, change[0], change[1], 1)), r); err == nil {
			t.Fatalf("accepted invalid %s", change[0])
		}
	}
}
