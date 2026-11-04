package oauthpage

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderSuccessIsSelfContainedAndBranded(t *testing.T) {
	out := Render(Page{Provider: "codex", Success: true, DashboardURL: DashboardPath, AutoClose: true})
	for _, want := range []string{
		"Signed in to ChatGPT",
		"You can close this tab. NeoProxy added this account to the pool.",
		`href="/neo/#/"`,
		"oklch(0.74 0.13 165)",
		"#0D0A13",
		"window.close",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("success page lacks %q", want)
		}
	}
	for _, external := range []string{"<link", "src=", "@import", "url("} {
		if strings.Contains(out, external) {
			t.Errorf("page references an external asset via %q", external)
		}
	}
}

func TestRenderFailureEscapesDetailAndNeverAutoCloses(t *testing.T) {
	out := Render(Page{Provider: "anthropic", Detail: `access_denied <script>alert(1)</script>`, DashboardURL: DashboardPath, AutoClose: true})
	if !strings.Contains(out, "Sign-in to Claude did not finish") || !strings.Contains(out, "Nothing was added to the pool") {
		t.Fatalf("failure wording missing: %s", out)
	}
	if strings.Contains(out, "<script>alert") || !strings.Contains(out, "&lt;script&gt;") {
		t.Fatal("provider-supplied detail was not escaped")
	}
	if strings.Contains(out, "window.close") {
		t.Fatal("failure page must stay open so the reason can be read")
	}
	if strings.Contains(out, "added this account") {
		t.Fatal("failure page claims success")
	}
}

func TestRenderLocalServerOmitsDashboardLinkAndRejectsUnsafeNoticeURL(t *testing.T) {
	out := Render(Page{Provider: "unknown-provider", Success: true, Notice: "Finish setup", NoticeURL: "javascript:alert(1)"})
	if strings.Contains(out, "/neo/#/") || !strings.Contains(out, "Return to the terminal") {
		t.Fatal("CLI-local page must not link to a dashboard on another origin")
	}
	if strings.Contains(out, "javascript:") || !strings.Contains(out, "Finish setup") {
		t.Fatal("notice URL must be http(s) only while the notice text stays")
	}
}

func TestWriteKeepsStatusAndHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, http.StatusBadRequest, Page{Provider: "devin", Detail: "missing authorization code"})
	if rec.Code != http.StatusBadRequest || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v", rec.Code, rec.Header())
	}
}
