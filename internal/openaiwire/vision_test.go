package openaiwire

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

func TestNativeTypedUserImageClampsOriginalDetail(t *testing.T) {
	model := registry.XaiAPIKeyNeoPiModel("grok-4.3")
	if model == nil || model.Compat == nil || model.Compat.SupportsImageDetailOriginal {
		t.Fatal("grok-4.3 must have image-detail-original disabled")
	}
	body := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"https://example.invalid/image.png","detail":"original"}]}]}`)
	context, options := FromResponses(body, model)
	shaped := BuildResponses(model, context, options)
	if got := gjson.GetBytes(shaped, "input.0.type").String(); got != "message" {
		t.Errorf("native type = %q, want message: %s", got, shaped)
	}
	if got := gjson.GetBytes(shaped, "input.0.content.0.detail").String(); got != "auto" {
		t.Errorf("image detail = %q, want auto: %s", got, shaped)
	}
}
