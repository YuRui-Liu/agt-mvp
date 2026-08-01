package mocksvc

import (
	"bytes"
	"encoding/base64"
	"errors"
	"html/template"
	"net/url"

	qrcode "github.com/skip2/go-qrcode"
)

type PosterModel struct {
	KuAIID         string
	Headline       string
	Tags           []string
	Encouragement  string
	ApplicationURL string
}

type QREncoder interface {
	Encode(text string) ([]byte, error)
}

type GoQREncoder struct{}

var ErrInvalidApplicationURL = errors.New("invalid local application URL")

func (GoQREncoder) Encode(text string) ([]byte, error) {
	return qrcode.Encode(text, qrcode.Medium, 256)
}

var posterTemplate = template.Must(template.New("poster").Funcs(template.FuncMap{
	"tagY": func(index int) int { return 420 + index*72 },
}).Parse(`<svg xmlns="http://www.w3.org/2000/svg" width="1080" height="1440" viewBox="0 0 1080 1440" role="img">
  <rect width="1080" height="1440" fill="#f6f2e8"/>
  <text x="80" y="130" font-size="38" fill="#28433a">{{.KuAIID}}</text>
  <text x="80" y="285" font-size="64" fill="#172d26">{{.Headline}}</text>
  {{range $index, $tag := .Tags}}<text x="80" y="{{tagY $index}}" font-size="34" fill="#3d695b">{{$tag}}</text>{{end}}
  <text x="80" y="760" font-size="36" fill="#28433a">{{.Encouragement}}</text>
  <image x="668" y="1030" width="256" height="256" href="{{.QRData}}"/>
</svg>`))

func RenderPoster(model PosterModel, encoder QREncoder) ([]byte, error) {
	if encoder == nil {
		return nil, errors.New("QR encoder required")
	}
	if err := validateApplicationURL(model.ApplicationURL); err != nil {
		return nil, err
	}
	png, err := encoder.Encode(model.ApplicationURL)
	if err != nil {
		return nil, err
	}
	data := struct {
		KuAIID        string
		Headline      string
		Tags          []string
		Encouragement string
		QRData        template.URL
	}{
		KuAIID:        model.KuAIID,
		Headline:      model.Headline,
		Tags:          append([]string(nil), model.Tags...),
		Encouragement: model.Encouragement,
		QRData:        template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png)),
	}
	var output bytes.Buffer
	if err := posterTemplate.Execute(&output, data); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func validateApplicationURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Opaque != "" ||
		parsed.User != nil || parsed.Fragment != "" || parsed.Path != "/application" {
		return ErrInvalidApplicationURL
	}
	switch parsed.Hostname() {
	case "127.0.0.1", "::1", "localhost":
	default:
		return ErrInvalidApplicationURL
	}
	query := parsed.Query()
	tickets, ok := query["ticket"]
	if !ok || len(query) != 1 || len(tickets) != 1 || tickets[0] == "" {
		return ErrInvalidApplicationURL
	}
	return nil
}
