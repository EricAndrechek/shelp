package provider

import "testing"

func TestNoteOfPerFamily(t *testing.T) {
	mk := func(id string) Provider {
		v, ok := variantByKey(id)
		if !ok {
			t.Fatalf("no variant %s", id)
		}
		return &shortcutsProvider{variant: v, name: "shelp"}
	}
	cases := []struct {
		p    Provider
		want string
	}{
		{mk("apple"), "Apple's cloud · smaller model"},
		{mk("apple-pro"), "Apple's cloud · larger model"},
		{mk("chatgpt"), "OpenAI, through Apple's extension"},
		{mk("apple-pro-web"), "Apple's cloud · larger model · looks things up online"},
		{&fmProvider{}, "this Mac, offline · fastest, weakest"},
		{&agentcliProvider{spec: cliSpec{name: "claude", label: "Claude Code"}, cliModel: "haiku"}, "Anthropic, your Claude plan · faster"},
		{&agentcliProvider{spec: cliSpec{name: "claude", label: "Claude Code"}, cliModel: "sonnet"}, "Anthropic, your Claude plan · stronger"},
		{&agentcliProvider{spec: cliSpec{name: "claude", label: "Claude Code"}, cliModel: "opus"}, "Anthropic, your Claude plan"},
		{&openaiProvider{id: "lmstudio", kind: LocalServer, label: "lmstudio"}, "your local LM Studio server"},
		{&openaiProvider{id: "ollama", kind: LocalServer, label: "ollama"}, "your local Ollama server"},
		{&openaiProvider{id: "llamacpp", kind: LocalServer, label: "llamacpp"}, "your local llama.cpp server"},
		{&openaiProvider{id: "mine", kind: LocalServer, label: "mine"}, "your local mine server"},
		{&openaiProvider{id: "groq:m", kind: API, label: "groq", apiKeyEnv: "GROQ_API_KEY", model: "m"}, "groq API, key in $GROQ_API_KEY"},
	}
	for _, c := range cases {
		if got := NoteOf(c.p); got != c.want {
			t.Errorf("NoteOf(%s) = %q, want %q", c.p.ID(), got, c.want)
		}
	}
	if got := LabelOf(&agentcliProvider{spec: cliSpec{name: "claude", label: "Claude Code"}, cliModel: "haiku"}); got != "Claude Haiku" {
		t.Errorf("label = %q", got)
	}
}
