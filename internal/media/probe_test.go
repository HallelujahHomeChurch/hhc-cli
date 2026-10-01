package media

import (
	"strings"
	"testing"
)

const sourceProbe = `{"streams":[{"codec_type":"video","width":1920,"height":1080,"sample_aspect_ratio":"1:1","avg_frame_rate":"60000/1001","r_frame_rate":"60000/1001","color_transfer":"bt709"},{"codec_type":"audio"}],"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"9000.000000"}}`

func TestSourceProbeFeedsBoundedNoUpscalePlan(t *testing.T) {
	source, err := ParseSourceProbe(strings.NewReader(sourceProbe))
	if err != nil || source.Width != 1920 || source.FrameRate < 59.9 || source.DurationSeconds != 9000 || !source.HasAudio {
		t.Fatalf("source=%+v err=%v", source, err)
	}
	plan, err := PlanSource(source, DefaultEncodeOptions())
	if err != nil || len(plan.Renditions) != 2 || plan.Renditions[0].FrameRate != 30 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	for _, tc := range []struct{ old, replacement string }{
		{`"format_name":"mov,mp4,m4a,3gp,3g2,mj2"`, `"format_name":"hls"`},
		{`"duration":"9000.000000"`, `"duration":"NaN"`},
		{`"duration":"9000.000000"`, `"duration":"Inf"`},
		{`"avg_frame_rate":"60000/1001"`, `"avg_frame_rate":"1/0"`},
		{`"sample_aspect_ratio":"1:1"`, `"sample_aspect_ratio":"N/A"`},
		{`"color_transfer":"bt709"`, `"color_transfer":"smpte2084"`},
		{`"codec_type":"audio"`, `"codec_type":"subtitle"`},
		{`"codec_type":"video"`, `"codec_type":"subtitle"`},
		{`"height":1080`, `"height":1080,"side_data_list":[{"rotation":90}]`},
		{`"height":1080`, `"height":1080,"disposition":{"attached_pic":1}`},
	} {
		_, err := ParseSourceProbe(strings.NewReader(strings.Replace(sourceProbe, tc.old, tc.replacement, 1)))
		if err == nil {
			t.Errorf("accepted unsupported source: %s", tc.replacement)
		}
	}
	for _, data := range []string{sourceProbe + `{}`, strings.Repeat(" ", (1<<20)+1) + sourceProbe} {
		if _, err := ParseSourceProbe(strings.NewReader(data)); err == nil {
			t.Fatal("unbounded/trailing probe accepted")
		}
	}
}
