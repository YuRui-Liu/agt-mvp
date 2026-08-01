package mocksvc

import (
	"bytes"
	"errors"
	"testing"
)

type recordingQREncoder struct {
	text string
	png  []byte
	err  error
}

func (e *recordingQREncoder) Encode(text string) ([]byte, error) {
	e.text = text
	return e.png, e.err
}

func TestPosterContainsQualitativeCopyAndNoScores(t *testing.T) {
	encoder := &recordingQREncoder{png: []byte("png")}
	svg, err := RenderPoster(PosterModel{
		KuAIID:         "KUAI-ABC123",
		Headline:       "善于拆解复杂目标",
		Tags:           []string{"目标引导", "工程协作", "持续迭代"},
		Encouragement:  "继续保持好奇与行动。",
		ApplicationURL: "http://127.0.0.1:4000/application?ticket=t1",
	}, encoder)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"score", "分数", "权重", "排名", "阈值"} {
		if bytes.Contains(bytes.ToLower(svg), []byte(forbidden)) {
			t.Fatalf("poster leaked %q", forbidden)
		}
	}
	if !bytes.Contains(svg, []byte("<svg")) || !bytes.Contains(svg, []byte("data:image/png;base64,")) {
		t.Fatal("poster or QR missing")
	}
	if encoder.text != "http://127.0.0.1:4000/application?ticket=t1" {
		t.Fatalf("QR content = %q", encoder.text)
	}
}

func TestPosterEscapesEveryTextField(t *testing.T) {
	encoder := &recordingQREncoder{png: []byte("png")}
	svg, err := RenderPoster(PosterModel{
		KuAIID:         `<script id="kuai">`,
		Headline:       `<script id="headline">`,
		Tags:           []string{`<script id="tag">`},
		Encouragement:  `<script id="encouragement">`,
		ApplicationURL: "http://127.0.0.1/application?ticket=x",
	}, encoder)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(svg, []byte("<script")) {
		t.Fatalf("unescaped SVG: %s", svg)
	}
	for _, escaped := range []string{"&lt;script"} {
		if !bytes.Contains(svg, []byte(escaped)) {
			t.Fatalf("missing escaped text %q: %s", escaped, svg)
		}
	}
}

func TestPosterPropagatesQRError(t *testing.T) {
	want := errors.New("encode failed")
	if _, err := RenderPoster(PosterModel{ApplicationURL: "http://127.0.0.1/application?ticket=t1"}, &recordingQREncoder{err: want}); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}

func TestPosterRequiresControlledLocalApplicationURL(t *testing.T) {
	valid := []string{
		"http://127.0.0.1:4000/application?ticket=t1",
		"http://localhost/application?ticket=t2",
		"http://[::1]:4000/application?ticket=t3",
	}
	for _, applicationURL := range valid {
		encoder := &recordingQREncoder{png: []byte("png")}
		if _, err := RenderPoster(PosterModel{ApplicationURL: applicationURL}, encoder); err != nil {
			t.Errorf("valid URL %q: %v", applicationURL, err)
		}
	}

	invalid := []string{
		"https://localhost/application?ticket=t1",
		"http://example.com/application?ticket=t1",
		"http://127.0.0.1/other?ticket=t1",
		"http://127.0.0.1/application",
		"http://127.0.0.1/application?ticket=",
		"http://127.0.0.1/application?ticket=t1&subject=sub-1",
		"http://user@127.0.0.1/application?ticket=t1",
		"http://127.0.0.1/application?ticket=t1#fragment",
	}
	for _, applicationURL := range invalid {
		encoder := &recordingQREncoder{png: []byte("png")}
		if _, err := RenderPoster(PosterModel{ApplicationURL: applicationURL}, encoder); !errors.Is(err, ErrInvalidApplicationURL) {
			t.Errorf("invalid URL %q error = %v", applicationURL, err)
		}
		if encoder.text != "" {
			t.Errorf("invalid URL %q reached QR encoder", applicationURL)
		}
	}
}
