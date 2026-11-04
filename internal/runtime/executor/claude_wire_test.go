package executor

import (
	"bytes"
	"testing"
)

func TestClaudeNeoPiBillingSignatureFinalFraming(t *testing.T) {
	for _, body := range []string{
		`{"messages":[{"role":"user","content":"cch=00000"}]}`,
		`{"system":[{"type":"text","text":"caller"},{"type":"text","text":"x-anthropic-billing-header: cch=00000;"}]}`,
		`{"system":[{"text":"x-anthropic-billing-header: cch=00000;","type":"text"}]}`,
		`{"system":[{"type":"text","text":"x-anthropic-billing-header: cch=abcde;"}]}`,
	} {
		input := []byte(body)
		if got := neoPiSignBody(input); string(got) != body {
			t.Fatalf("source would not patch this body: %s", got)
		}
	}
	body := []byte(`{"messages":[{"role":"user","content":"hello"}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=test; cch=00000;"}]}`)
	before := append([]byte(nil), body...)
	got := neoPiSignBody(body)
	if bytes.Equal(got, before) || len(got) != len(before) {
		t.Fatalf("anchored placeholder was not patched in place: %s", got)
	}
	start := bytes.Index(before, []byte("cch=00000")) + 4
	copy(before[start:start+5], got[start:start+5])
	if !bytes.Equal(before, got) {
		t.Fatal("signature framing changed bytes outside the five-digit field")
	}
	if twice := neoPiSignBody(append([]byte(nil), got...)); !bytes.Equal(twice, got) {
		t.Fatal("pre-signed caller body was re-signed")
	}
}

func TestClaudeNeoPiToolIDProjection(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"call_1", "call_1"},
		{"tool:1#🦊", "tool_1___"},
		{"", "toolu_27k1wwwhf13t"},
	} {
		if got := neoPiNormalizeToolID(tc.input); got != tc.want {
			t.Errorf("source ID %q => %q, want %q", tc.input, got, tc.want)
		}
	}
	var ids neoPiToolIDState
	ids.beginTurn()
	if got := ids.call("x:y", true); got != "x_y" {
		t.Fatal(got)
	}
	if got := ids.call("x_y", true); got != "x_y_dup1" {
		t.Fatal(got)
	}
	if first, second := ids.result("x:y"), ids.result("x_y"); first != "x_y" || second != "x_y_dup1" {
		t.Fatalf("colliding source ID pairs = %q %q", first, second)
	}
	ids.beginTurn()
	if got := ids.call("x:y", true); got != "x_y_dup2" {
		t.Fatal(got)
	}
	if got := ids.result("x:y"); got != "x_y_dup2" {
		t.Fatal(got)
	}
}
