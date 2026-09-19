package main

import "testing"

func TestParseNativeMediaMeta(t *testing.T) {
	// Real AC004.db join row (video): 4K, 100fps (100000/1000), duration 24s.
	out := []byte(`[{"star":2,"highlight":1,"duration":24000,"resolution_width":3840,"resolution_height":2160,"frame_num":100000,"frame_den":1000,"encode_format":2,"steady_mode":2,"nd_value":0,"ev_bias":17}]`)
	meta, err := parseNativeMediaMeta(out)
	if err != nil || meta == nil || !meta.Indexed {
		t.Fatalf("parse failed: %+v %v", meta, err)
	}
	if meta.Rating == nil || *meta.Rating != 2 {
		t.Fatalf("rating: %+v", meta.Rating)
	}
	if meta.Highlight == nil || !*meta.Highlight {
		t.Fatalf("highlight flag: %+v", meta.Highlight)
	}
	if meta.FPS == nil || *meta.FPS != 100 {
		t.Fatalf("fps division wrong: %+v", meta.FPS)
	}
	if meta.Width == nil || *meta.Width != 3840 {
		t.Fatalf("width: %+v", meta.Width)
	}

	// Image row: no video_info (LEFT JOIN nulls), highlight 0 -> false.
	img := []byte(`[{"star":0,"highlight":0,"duration":null,"resolution_width":null,"resolution_height":null,"frame_num":null,"frame_den":null,"encode_format":null,"steady_mode":null,"nd_value":null,"ev_bias":null}]`)
	meta, err = parseNativeMediaMeta(img)
	if err != nil || !meta.Indexed || meta.FPS != nil || meta.Width != nil {
		t.Fatalf("image row mishandled: %+v %v", meta, err)
	}
	if meta.Highlight == nil || *meta.Highlight {
		t.Fatalf("highlight 0 should be false: %+v", meta.Highlight)
	}

	// Zero-denominator must not divide; not in index -> indexed=false.
	zero := []byte(`[{"frame_num":30000,"frame_den":0}]`)
	if meta, err = parseNativeMediaMeta(zero); err != nil || meta.FPS != nil {
		t.Fatalf("zero den produced fps: %+v %v", meta, err)
	}
	if meta, err = parseNativeMediaMeta([]byte("")); err != nil || meta.Indexed {
		t.Fatalf("empty should be not-indexed: %+v %v", meta, err)
	}
	if meta, err = parseNativeMediaMeta([]byte("[]")); err != nil || meta.Indexed {
		t.Fatalf("no rows should be not-indexed: %+v %v", meta, err)
	}
}
