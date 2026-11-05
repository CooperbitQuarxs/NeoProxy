package helps

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/tidwall/gjson"
)

// FactoryDroidMaxEditImages is FACTORY_DROID_MAX_EDIT_IMAGES: the reference
// images Factory's JSON edits contract accepts per request.
const FactoryDroidMaxEditImages = 16

// FactoryDroidMaxReferenceBytes is FACTORY_DROID_MAX_REFERENCE_BYTES: the
// decoded bytes the CLI accepts per reference image.
const FactoryDroidMaxReferenceBytes = 5 * 1024 * 1024

// factoryDroidEditMediaTypes is FACTORY_DROID_EDIT_MEDIA_TYPES.
var factoryDroidEditMediaTypes = []string{"image/png", "image/jpeg", "image/webp"}

// imageMIMEType is pi-ai's IMAGE_MIME_TYPE.
var imageMIMEType = regexp.MustCompile(`(?i)^image/[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]{0,126}$`)

var base64ImageData = regexp.MustCompile(`^([A-Za-z0-9+/]+)(={0,2})$`)

const base64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

// FactoryDroidImageInput is NeoPi's ImageInput: base64 data and its MIME
// type, as the caller supplied them.
type FactoryDroidImageInput struct {
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

// FactoryDroidImageInputs reads reference images given as data URLs. NeoPi's
// image requests carry references inline only, so a remote URL is refused.
func FactoryDroidImageInputs(urls []string) ([]FactoryDroidImageInput, error) {
	inputs := make([]FactoryDroidImageInput, 0, len(urls))
	for index, url := range urls {
		rest, ok := strings.CutPrefix(url, "data:")
		meta, data, found := strings.Cut(rest, ",")
		mimeType, base64Encoded := strings.CutSuffix(meta, ";base64")
		if !ok || !found || !base64Encoded {
			return nil, fmt.Errorf("factory-droid reference image %d must be an inline base64 data URL", index+1)
		}
		inputs = append(inputs, FactoryDroidImageInput{Data: data, MimeType: mimeType})
	}
	return inputs, nil
}

// FactoryDroidValidateReferences is validateReferences: it rejects the
// references the proxy would refuse before any bytes leave. openAIEdit is a
// GPT Image request with references, bound by the JSON edits contract.
func FactoryDroidValidateReferences(provider string, inputs []FactoryDroidImageInput, openAIEdit bool) error {
	if openAIEdit && len(inputs) > FactoryDroidMaxEditImages {
		return fmt.Errorf("%s image edits accept up to %d reference images; got %d", provider, FactoryDroidMaxEditImages, len(inputs))
	}
	for index, image := range inputs {
		if reason := base64ImageDataFailureReason(image.Data); reason != "" {
			return fmt.Errorf("%s", reason)
		}
		if !imageMIMEType.MatchString(image.MimeType) {
			return fmt.Errorf("invalid image MIME type")
		}
		if bytes := base64ByteLength(image.Data); bytes > FactoryDroidMaxReferenceBytes {
			return fmt.Errorf("%s reference image %d is %d bytes; the limit is %d (5 MiB)", provider, index+1, bytes, FactoryDroidMaxReferenceBytes)
		}
		if openAIEdit && !slices.Contains(factoryDroidEditMediaTypes, strings.ToLower(image.MimeType)) {
			return fmt.Errorf("%s image edits accept %s references; got %s", provider, strings.Join(factoryDroidEditMediaTypes, ", "), image.MimeType)
		}
	}
	return nil
}

// base64ImageDataFailureReason is pi-utils' base64ImageDataFailureReason:
// canonical standard-alphabet base64 with consistent padding.
func base64ImageDataFailureReason(data string) string {
	if data == "" {
		return "empty image data"
	}
	match := base64ImageData.FindStringSubmatch(data)
	if match == nil {
		return "invalid base64 image data"
	}
	encoded, padding := match[1], len(match[2])
	remainder := len(encoded) % 4
	if remainder == 1 || (padding == 1 && remainder != 3) || (padding == 2 && remainder != 2) {
		return "invalid base64 image data"
	}
	lastSextet := strings.IndexByte(base64Alphabet, encoded[len(encoded)-1])
	if (remainder == 2 && lastSextet&0b1111 != 0) || (remainder == 3 && lastSextet&0b11 != 0) {
		return "invalid base64 image data"
	}
	return ""
}

// base64ByteLength is Node's Buffer.byteLength(data, "base64").
func base64ByteLength(data string) int {
	n := len(data)
	if n > 0 && data[n-1] == '=' {
		n--
	}
	if n > 1 && data[n-1] == '=' {
		n--
	}
	return n * 3 / 4
}

// FactoryDroidImagesBody is the GPT Image body: the prompt alone, or with
// reference images inline for /images/edits, and the Droid identity on the
// proxy-only instructions field.
func FactoryDroidImagesBody(model, prompt string, count int64, inputs []FactoryDroidImageInput) []byte {
	fields := []jsjson.Field{{Name: "model", Value: jsjson.Marshal(model)}, {Name: "prompt", Value: jsjson.Marshal(prompt)},
		{Name: "n", Value: jsjson.Marshal(count)}, {Name: "instructions", Value: jsjson.Marshal(FactoryDroidSystemPrefix)}}
	if len(inputs) > 0 {
		images := make([]json.RawMessage, 0, len(inputs))
		for _, image := range inputs {
			images = append(images, jsjson.Object(jsjson.Field{Name: "media_type", Value: jsjson.Marshal(strings.ToLower(image.MimeType))},
				jsjson.Field{Name: "data", Value: jsjson.Marshal(image.Data)}))
		}
		fields = append(fields, jsjson.Field{Name: "input_images", Value: jsjson.Marshal(images)})
	}
	return jsjson.Object(fields...)
}

// factoryDroidGeminiImageSystem is factory-droid-gemini-system.md rendered
// for a generation or an edit.
func factoryDroidGeminiImageSystem(edit bool) string {
	directive := "Produce a single image that faithfully matches the description in the user message."
	if edit {
		directive = "Produce a single image that applies the description in the user message to the images that follow it. " +
			"Treat those images as the subject to change and keep everything the description does not mention."
	}
	return FactoryDroidSystemPrefix + " " + directive + " Do not ask clarifying questions."
}

// FactoryDroidGeminiImageBody is the Gemini image body: the prompt then the
// references, the Droid identity opening the generate or edit directive, and
// TEXT and IMAGE output at the CLI's sampling.
func FactoryDroidGeminiImageBody(model, prompt string, inputs []FactoryDroidImageInput, aspectRatio, imageSize string) []byte {
	parts := []json.RawMessage{jsjson.Object(jsjson.Field{Name: "text", Value: jsjson.Marshal(prompt)})}
	for _, image := range inputs {
		parts = append(parts, jsjson.Object(jsjson.Field{Name: "inlineData", Value: jsjson.Marshal(image)}))
	}
	contents := jsjson.Marshal([]json.RawMessage{jsjson.Object(jsjson.Field{Name: "role", Value: jsjson.Marshal("user")},
		jsjson.Field{Name: "parts", Value: jsjson.Marshal(parts)})})
	system := jsjson.Object(jsjson.Field{Name: "parts", Value: jsjson.Marshal([]json.RawMessage{
		jsjson.Object(jsjson.Field{Name: "text", Value: jsjson.Marshal(factoryDroidGeminiImageSystem(len(inputs) > 0))})})})
	config := []jsjson.Field{{Name: "temperature", Value: jsjson.Marshal(1)}, {Name: "topP", Value: jsjson.Marshal(0.95)},
		{Name: "topK", Value: jsjson.Marshal(64)}, {Name: "responseModalities", Value: jsjson.Marshal([]string{"TEXT", "IMAGE"})}}
	if aspectRatio != "" || imageSize != "" {
		var imageConfig []jsjson.Field
		if aspectRatio != "" {
			imageConfig = append(imageConfig, jsjson.Field{Name: "aspectRatio", Value: jsjson.Marshal(aspectRatio)})
		}
		if imageSize != "" {
			imageConfig = append(imageConfig, jsjson.Field{Name: "imageSize", Value: jsjson.Marshal(imageSize)})
		}
		config = append(config, jsjson.Field{Name: "imageConfig", Value: jsjson.Object(imageConfig...)})
	}
	return jsjson.Object(jsjson.Field{Name: "model", Value: jsjson.Marshal(model)}, jsjson.Field{Name: "contents", Value: contents},
		jsjson.Field{Name: "systemInstruction", Value: system}, jsjson.Field{Name: "generationConfig", Value: jsjson.Object(config...)})
}

// FactoryDroidGeminiImageResult folds a Gemini image stream as
// geminiImageResult does, with the prompt block and finish reason the
// transport checks.
type FactoryDroidGeminiImageResult struct {
	Images       []FactoryDroidImageInput
	text         strings.Builder
	usage        gjson.Result
	BlockReason  string
	FinishReason string
}

// Observe folds one stream chunk.
func (r *FactoryDroidGeminiImageResult) Observe(chunk []byte) {
	root := gjson.ParseBytes(chunk)
	if block := root.Get("promptFeedback.blockReason"); block.Type == gjson.String {
		r.BlockReason = block.String()
	}
	if finish := root.Get("candidates.0.finishReason"); finish.Type == gjson.String {
		r.FinishReason = finish.String()
	}
	var texts []string
	for _, candidate := range root.Get("candidates").Array() {
		for _, part := range candidate.Get("content.parts").Array() {
			if text := part.Get("text").String(); text != "" {
				texts = append(texts, text)
			}
			data, mimeType := part.Get("inlineData.data").String(), part.Get("inlineData.mimeType").String()
			if data != "" && mimeType != "" {
				r.Images = append(r.Images, FactoryDroidImageInput{Data: data, MimeType: mimeType})
			}
		}
	}
	r.text.WriteString(strings.Join(texts, "\n"))
	if usage := root.Get("usageMetadata"); usage.Exists() {
		r.usage = usage
	}
}

// Text is the model's trimmed text answer.
func (r *FactoryDroidGeminiImageResult) Text() string { return strings.TrimSpace(r.text.String()) }

// Terminal is the stream check: a prompt block ends without candidates;
// anything else must reach a terminal STOP.
func (r *FactoryDroidGeminiImageResult) Terminal() error {
	if r.BlockReason != "" || r.FinishReason == "STOP" {
		return nil
	}
	if r.FinishReason != "" {
		return fmt.Errorf("Factory Gemini image generation stopped with %s", r.FinishReason)
	}
	return fmt.Errorf("Factory Gemini image stream ended before a finish reason")
}

// NoImageDetail is why a finished response carried no image.
func (r *FactoryDroidGeminiImageResult) NoImageDetail() string {
	switch {
	case r.BlockReason != "":
		return "the prompt was blocked (" + r.BlockReason + ")"
	case r.Text() != "":
		return "the model answered with text only: " + r.Text()
	}
	return "the response carried no image"
}

// ImagesResponse is the OpenAI Images response for the generated images,
// with the stream's token usage.
func (r *FactoryDroidGeminiImageResult) ImagesResponse(created int64) []byte {
	data := make([]json.RawMessage, 0, len(r.Images))
	for _, image := range r.Images {
		data = append(data, jsjson.Object(jsjson.Field{Name: "b64_json", Value: jsjson.Marshal(image.Data)}))
	}
	input, output := r.usage.Get("promptTokenCount").Int(), r.usage.Get("candidatesTokenCount").Int()
	usage := jsjson.Object(jsjson.Field{Name: "input_tokens", Value: jsjson.Marshal(input)},
		jsjson.Field{Name: "output_tokens", Value: jsjson.Marshal(output)}, jsjson.Field{Name: "total_tokens", Value: jsjson.Marshal(input + output)})
	return jsjson.Object(jsjson.Field{Name: "created", Value: jsjson.Marshal(created)}, jsjson.Field{Name: "data", Value: jsjson.Marshal(data)},
		jsjson.Field{Name: "usage", Value: usage})
}

// FactoryDroidImagesCount is how many images an Images API reply carries.
func FactoryDroidImagesCount(body []byte) int {
	count := 0
	for _, item := range gjson.GetBytes(body, "data").Array() {
		if b64, url := item.Get("b64_json"), item.Get("url"); b64.Type == gjson.String && b64.String() != "" || url.Type == gjson.String && url.String() != "" {
			count++
		}
	}
	return count
}
