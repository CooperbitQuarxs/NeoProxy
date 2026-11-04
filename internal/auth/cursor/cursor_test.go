package cursor

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

func modelDetails(id, name string, maxMode bool) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, id)
	b = protowire.AppendTag(b, 4, protowire.BytesType)
	b = protowire.AppendString(b, name)
	if maxMode {
		b = protowire.AppendTag(b, 7, protowire.VarintType)
		b = protowire.AppendVarint(b, 1)
	}
	return b
}

func TestDecodeUsableModelsRawAndEnveloped(t *testing.T) {
	var body []byte
	for _, m := range [][]byte{modelDetails("composer-2.5", "Composer 2.5", false), modelDetails("claude-opus-5-5-high-fast", "Claude Opus 5.5 1M High Fast", true), modelDetails("composer-2.5", "dup", false)} {
		body = protowire.AppendTag(body, 1, protowire.BytesType)
		body = protowire.AppendBytes(body, m)
	}
	envelope := make([]byte, 5, 5+len(body))
	binary.BigEndian.PutUint32(envelope[1:], uint32(len(body)))
	envelope = append(envelope, body...)

	for name, input := range map[string][]byte{"raw": body, "enveloped": envelope} {
		got, err := DecodeUsableModels(input)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(got) != 2 || got[0] != (UsableModel{ID: "composer-2.5", DisplayName: "Composer 2.5"}) || !got[1].MaxMode {
			t.Fatalf("%s = %+v", name, got)
		}
	}
}

func TestAccessTokenClaims(t *testing.T) {
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	payload, _ := json.Marshal(map[string]any{"sub": "auth0|user_abc", "exp": exp.Unix()})
	token := "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
	if got := UserID(token); got != "user_abc" {
		t.Fatalf("UserID = %q", got)
	}
	if got := TokenExpiry(token); !got.Equal(exp) {
		t.Fatalf("TokenExpiry = %v, want %v", got, exp)
	}
	if got := CredentialFileName("", "user_abc"); got != "cursor-user_abc.json" {
		t.Fatalf("CredentialFileName = %q", got)
	}
	if got := CredentialFileName("a b@example.test", "user_abc"); got != "cursor-a_b@example.test.json" {
		t.Fatalf("CredentialFileName(email) = %q", got)
	}
}
