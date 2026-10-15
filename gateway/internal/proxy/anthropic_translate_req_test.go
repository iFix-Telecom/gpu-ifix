package proxy

// Testes do tradutor de ENTRADA do passo ① (plano 23-08, HAIKU-01/HAIKU-05):
// OpenAI chat/completions → Anthropic Messages (POST /v1/messages).
//
// Pontos que o 23-RESEARCH marcou [ASSUMED]/HIPÓTESE, conferidos na doc
// oficial em 2026-10-15 (FATO, com fonte):
//
//   - A1 `disable_parallel_tool_use`: campo booleano DENTRO de `tool_choice`
//     (auto/any/tool). "Set `disable_parallel_tool_use: true` in the
//     `tool_choice` field to ensure that Claude uses at most one tool."
//     https://platform.claude.com/docs/en/agents-and-tools/tool-use/parallel-tool-use
//     https://platform.claude.com/docs/en/api/messages (ToolChoiceAny/Auto/Tool)
//   - A1 structured outputs: `output_config.format = {"type":"json_schema",
//     "schema": {...}}` (schema direto, sem wrapper name/strict).
//     https://platform.claude.com/docs/en/build-with-claude/structured-outputs
//     `json_object` não tem equivalente → instrução no system (decisão do
//     research, Padrão 1).
//   - Effort: `output_config.effort` ∈ low|medium|high|xhigh|max; default do
//     Haiku 5.5 = medium.
//     https://platform.claude.com/docs/en/build-with-claude/effort
//   - Effort "off": o Haiku 5.5 "accepts `thinking: {type: "disabled"}` at
//     effort `high` or below. At `xhigh` or `max` effort, that combination
//     returns a 400 error." → "off" vira thinking disabled SEM
//     output_config.effort (default medium ≤ high). Nenhum fallback "low".
//     https://platform.claude.com/docs/en/build-with-claude/thinking
//     (tabela por modelo: "Claude Haiku 5.5 | ... | Thinking off at `high`
//     effort or below"). Consequência: o modo ToolLoopMode "disable" EXISTE.
//   - Mid-conversation system messages: suportadas no Haiku 5.5 sem beta
//     header. Regra de posição: "must immediately follow a `user` turn ... and
//     must either be the last entry in `messages` or be immediately followed by
//     an `assistant` turn. A `user` message that carries `tool_result` blocks
//     counts". Outra posição → 400.
//     https://platform.claude.com/docs/en/build-with-claude/mid-conversation-system-messages
//   - Prompt caching automático: um `cache_control` top-level; combina com
//     breakpoint explícito (usa 1 dos 4 slots).
//     https://platform.claude.com/docs/en/build-with-claude/prompt-caching
//   - Tool use / forced tool_choice aceito no Haiku 5.5 (sem thinking):
//     https://platform.claude.com/docs/en/models/haiku-5-5/migration-guide
//     https://platform.claude.com/docs/en/agents-and-tools/tool-use/implement-tool-use
//
// HIPÓTESE H10 (não resolvida pela doc, que manda devolver blocos thinking
// intactos em tool loops): omitir os blocos thinking na continuação não dá
// 400. O translator não gera blocos thinking (cliente OpenAI não os tem); o
// gate ao vivo do 23-18 decide entre omit/disable/skip antes de ligar o ①.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func haikuOpts() AnthropicRequestOptions {
	return AnthropicRequestOptions{
		Model:            "claude-haiku-5-5",
		Effort:           "high",
		DefaultMaxTokens: 16000,
		ThinkingHeadroom: 4096,
		MaxTokensCap:     128000,
		Cache:            true,
	}
}

// trq traduz e decodifica a saída como map genérico.
func trq(t *testing.T, in string, opts AnthropicRequestOptions) map[string]any {
	t.Helper()
	out, err := TranslateOpenAIToMessages([]byte(in), opts)
	if err != nil {
		t.Fatalf("TranslateOpenAIToMessages: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("saída não é JSON: %v (%s)", err, out)
	}
	return m
}

func jsonEq(t *testing.T, got any, wantJSON string) {
	t.Helper()
	var want any
	if err := json.Unmarshal([]byte(wantJSON), &want); err != nil {
		t.Fatalf("want inválido: %v", err)
	}
	gb, _ := json.Marshal(got)
	var g any
	_ = json.Unmarshal(gb, &g)
	if !reflect.DeepEqual(g, want) {
		t.Fatalf("diferente\n got: %s\nwant: %s", gb, wantJSON)
	}
}

func msgsOf(t *testing.T, m map[string]any) []any {
	t.Helper()
	msgs, ok := m["messages"].([]any)
	if !ok {
		t.Fatalf("messages ausente: %v", m)
	}
	return msgs
}

func goldenCompare(t *testing.T, opts AnthropicRequestOptions, goldenName string) {
	t.Helper()
	in, err := os.ReadFile(filepath.Join("testdata", "anthropic", "req_openai_tool_loop.json"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "anthropic", goldenName))
	if err != nil {
		t.Fatal(err)
	}
	out, err := TranslateOpenAIToMessages(in, opts)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	var g, w any
	if err := json.Unmarshal(out, &g); err != nil {
		t.Fatalf("saída inválida: %v", err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("golden inválido: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("golden %s diverge\n got: %s", goldenName, out)
	}
}

func TestAnthropicTranslateRequest_GoldenToolLoop(t *testing.T) {
	t.Run("omit_default", func(t *testing.T) {
		goldenCompare(t, haikuOpts(), "req_messages_tool_loop.golden.json")
	})
	t.Run("omit_explicit", func(t *testing.T) {
		o := haikuOpts()
		o.ToolLoopMode = "omit"
		goldenCompare(t, o, "req_messages_tool_loop.golden.json")
	})
	t.Run("disable_continuation", func(t *testing.T) {
		o := haikuOpts()
		o.ToolLoopMode = "disable"
		goldenCompare(t, o, "req_messages_tool_loop_disable.golden.json")
	})
}

func TestAnthropicTranslateRequest(t *testing.T) {
	tools := `"tools":[{"type":"function","function":{"name":"x","description":"d","parameters":{"type":"object","properties":{}}}}]`

	t.Run("tool_loop_followed_by_user_merges_into_one_user_turn", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"x","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"r"},{"role":"user","content":"e agora?"}],`+tools+`}`, haikuOpts())
		msgs := msgsOf(t, m)
		if len(msgs) != 3 {
			t.Fatalf("esperava 3 turnos, veio %d: %v", len(msgs), msgs)
		}
		jsonEq(t, msgs[2], `{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"r"},{"type":"text","text":"e agora?"}]}`)
	})

	t.Run("tool_choice_required_any", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"}],`+tools+`,"tool_choice":"required"}`, haikuOpts())
		jsonEq(t, m["tool_choice"], `{"type":"any"}`)
	})
	t.Run("tool_choice_forced_named", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"}],`+tools+`,"tool_choice":{"type":"function","function":{"name":"x"}}}`, haikuOpts())
		jsonEq(t, m["tool_choice"], `{"type":"tool","name":"x"}`)
	})
	t.Run("tool_choice_none", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"}],`+tools+`,"tool_choice":"none"}`, haikuOpts())
		jsonEq(t, m["tool_choice"], `{"type":"none"}`)
	})
	t.Run("tool_choice_auto", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"}],`+tools+`,"tool_choice":"auto"}`, haikuOpts())
		jsonEq(t, m["tool_choice"], `{"type":"auto"}`)
	})
	t.Run("parallel_tool_calls_false", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"}],`+tools+`,"tool_choice":"auto","parallel_tool_calls":false}`, haikuOpts())
		jsonEq(t, m["tool_choice"], `{"type":"auto","disable_parallel_tool_use":true}`)
	})
	t.Run("tool_strict_preserved_and_missing_parameters", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"}],"tools":[{"type":"function","function":{"name":"y","strict":true}}]}`, haikuOpts())
		jsonEq(t, m["tools"], `[{"name":"y","strict":true,"input_schema":{"type":"object","properties":{}}}]`)
	})
	t.Run("tool_choice_dropped_without_tools", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"}],"tool_choice":"auto"}`, haikuOpts())
		if _, ok := m["tool_choice"]; ok {
			t.Fatalf("tool_choice sem tools deveria sumir: %v", m["tool_choice"])
		}
	})

	t.Run("response_format_json_schema", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"}],"response_format":{"type":"json_schema","json_schema":{"name":"r","strict":true,"schema":{"type":"object","properties":{"ok":{"type":"boolean"}}}}}}`, haikuOpts())
		jsonEq(t, m["output_config"], `{"effort":"high","format":{"type":"json_schema","schema":{"type":"object","properties":{"ok":{"type":"boolean"}}}}}`)
	})
	t.Run("response_format_json_object", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"system","content":"regras"},{"role":"user","content":"a"}],"response_format":{"type":"json_object"}}`, haikuOpts())
		jsonEq(t, m["system"], `[{"type":"text","text":"regras"},{"type":"text","text":"Responda somente com um objeto JSON válido.","cache_control":{"type":"ephemeral"}}]`)
		oc, _ := m["output_config"].(map[string]any)
		if _, ok := oc["format"]; ok {
			t.Fatal("json_object não deveria virar output_config.format")
		}
	})

	t.Run("image_data_url_and_http", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":[{"type":"text","text":"veja"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}},{"type":"image_url","image_url":{"url":"https://ex.com/a.jpg"}}]}]}`, haikuOpts())
		jsonEq(t, msgsOf(t, m)[0], `{"role":"user","content":[{"type":"text","text":"veja"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}},{"type":"image","source":{"type":"url","url":"https://ex.com/a.jpg"}}]}`)
	})

	t.Run("rejected_and_unknown_params_removed", func(t *testing.T) {
		in := `{"messages":[{"role":"user","content":"a"}],"temperature":0,"top_p":1,"top_k":5,"frequency_penalty":0.1,"presence_penalty":0.2,"seed":7,"n":1,"user":"u","metadata":{"a":1},"store":true,"reasoning":{"enabled":false},"reasoning_effort":"low","chat_template_kwargs":{"enable_thinking":false},"stream_options":{"include_usage":true},"provider":{"order":["x"]},"logit_bias":{"1":1},"logprobs":true,"top_logprobs":2,"campo_desconhecido":1}`
		m := trq(t, in, haikuOpts())
		for _, k := range []string{"temperature", "top_p", "top_k", "frequency_penalty", "presence_penalty", "seed", "n", "user", "metadata", "store", "reasoning", "reasoning_effort", "chat_template_kwargs", "stream_options", "provider", "logit_bias", "logprobs", "top_logprobs", "campo_desconhecido"} {
			if _, ok := m[k]; ok {
				t.Errorf("%s não deveria chegar à Anthropic", k)
			}
		}
	})

	t.Run("trailing_assistant_gets_prossiga", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`, haikuOpts())
		msgs := msgsOf(t, m)
		jsonEq(t, msgs[len(msgs)-1], `{"role":"user","content":[{"type":"text","text":"Prossiga."}]}`)
	})

	t.Run("mid_system_native_after_user_before_assistant", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"system","content":"s0"},{"role":"user","content":"a"},{"role":"system","content":"s1"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`, haikuOpts())
		msgs := msgsOf(t, m)
		jsonEq(t, msgs[1], `{"role":"system","content":[{"type":"text","text":"s1"}]}`)
		jsonEq(t, m["system"], `[{"type":"text","text":"s0","cache_control":{"type":"ephemeral"}}]`)
	})
	t.Run("mid_system_native_last_after_tool_result", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"x","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"r"},{"role":"system","content":"lembrete"}],`+tools+`}`, haikuOpts())
		msgs := msgsOf(t, m)
		jsonEq(t, msgs[len(msgs)-1], `{"role":"system","content":[{"type":"text","text":"lembrete"}]}`)
	})
	t.Run("mid_system_between_users_folded_into_user", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"},{"role":"system","content":"s1"},{"role":"user","content":"c"}]}`, haikuOpts())
		msgs := msgsOf(t, m)
		if len(msgs) != 1 {
			t.Fatalf("esperava 1 turno user mesclado: %v", msgs)
		}
		jsonEq(t, msgs[0], `{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"s1"},{"type":"text","text":"c"}]}`)
	})
	t.Run("mid_system_after_assistant_before_user_folded_into_next_user", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"system","content":"s1"},{"role":"user","content":"c"}]}`, haikuOpts())
		msgs := msgsOf(t, m)
		jsonEq(t, msgs[2], `{"role":"user","content":[{"type":"text","text":"s1"},{"type":"text","text":"c"}]}`)
	})
	t.Run("system_without_adjacent_user_goes_to_top_level", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"system","content":"s0"},{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"system","content":"fim"}]}`, haikuOpts())
		jsonEq(t, m["system"], `[{"type":"text","text":"s0"},{"type":"text","text":"fim","cache_control":{"type":"ephemeral"}}]`)
		msgs := msgsOf(t, m)
		for _, x := range msgs {
			if x.(map[string]any)["role"] == "system" {
				t.Fatalf("system sem user adjacente não pode ficar em messages: %v", msgs)
			}
		}
		// conversa não pode terminar em assistant (prefill 400)
		jsonEq(t, msgs[len(msgs)-1], `{"role":"user","content":[{"type":"text","text":"Prossiga."}]}`)
	})
	t.Run("system_between_two_assistants_goes_to_top_level", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"system","content":"meio"},{"role":"assistant","content":"c"},{"role":"user","content":"d"}]}`, haikuOpts())
		jsonEq(t, m["system"], `[{"type":"text","text":"meio","cache_control":{"type":"ephemeral"}}]`)
	})

	t.Run("effort_high_and_max_tokens_headroom", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"}],"max_tokens":1000}`, haikuOpts())
		jsonEq(t, m["output_config"], `{"effort":"high"}`)
		jsonEq(t, m["max_tokens"], `5096`)
	})
	t.Run("max_completion_tokens_wins", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"}],"max_tokens":50,"max_completion_tokens":1000}`, haikuOpts())
		jsonEq(t, m["max_tokens"], `5096`)
	})
	t.Run("max_tokens_default", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"}]}`, haikuOpts())
		jsonEq(t, m["max_tokens"], `16000`)
	})
	t.Run("max_tokens_clamped", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"}],"max_tokens":200000}`, haikuOpts())
		jsonEq(t, m["max_tokens"], `128000`)
	})
	t.Run("effort_low_no_headroom", func(t *testing.T) {
		o := haikuOpts()
		o.Effort = "low"
		m := trq(t, `{"messages":[{"role":"user","content":"a"}],"max_tokens":1000}`, o)
		jsonEq(t, m["max_tokens"], `1000`)
		jsonEq(t, m["output_config"], `{"effort":"low"}`)
	})
	t.Run("effort_off_disables_thinking", func(t *testing.T) {
		// Regra escolhida (FATO, platform.claude.com/docs/en/build-with-claude/thinking):
		// Haiku 5.5 aceita thinking disabled em effort ≤ high → "off" = disabled
		// e sem output_config.effort. Nunca "low" calado.
		o := haikuOpts()
		o.Effort = "off"
		m := trq(t, `{"messages":[{"role":"user","content":"a"}],"max_tokens":1000}`, o)
		jsonEq(t, m["thinking"], `{"type":"disabled"}`)
		if _, ok := m["output_config"]; ok {
			t.Fatalf("effort off não pode mandar output_config: %v", m["output_config"])
		}
		jsonEq(t, m["max_tokens"], `1000`)
	})
	t.Run("effort_empty_not_set", func(t *testing.T) {
		o := haikuOpts()
		o.Effort = ""
		m := trq(t, `{"messages":[{"role":"user","content":"a"}]}`, o)
		if _, ok := m["output_config"]; ok {
			t.Fatal("effort vazio não deveria setar output_config")
		}
		if _, ok := m["thinking"]; ok {
			t.Fatal("effort vazio não deveria setar thinking")
		}
	})

	t.Run("stream_and_model", func(t *testing.T) {
		m := trq(t, `{"model":"deepseek-flash","stream":true,"messages":[{"role":"user","content":"a"}]}`, haikuOpts())
		jsonEq(t, m["stream"], `true`)
		jsonEq(t, m["model"], `"claude-haiku-5-5"`)
	})
	t.Run("assistant_text_and_tool_calls", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"vou consultar","tool_calls":[{"id":"c1","type":"function","function":{"name":"x","arguments":"{\"k\":1}"}}]},{"role":"tool","tool_call_id":"c1","content":"r"}],`+tools+`}`, haikuOpts())
		jsonEq(t, msgsOf(t, m)[1], `{"role":"assistant","content":[{"type":"text","text":"vou consultar"},{"type":"tool_use","id":"c1","name":"x","input":{"k":1}}]}`)
	})
	t.Run("invalid_arguments_become_empty_object", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"x","arguments":"{quebrado"}},{"id":"c2","type":"function","function":{"name":"x","arguments":""}}]},{"role":"tool","tool_call_id":"c1","content":"r"},{"role":"tool","tool_call_id":"c2","content":"r"}],`+tools+`}`, haikuOpts())
		jsonEq(t, msgsOf(t, m)[1], `{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"x","input":{}},{"type":"tool_use","id":"c2","name":"x","input":{}}]}`)
	})
	t.Run("no_thinking_blocks_generated", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"user","content":"a"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"x","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"r"}],`+tools+`}`, haikuOpts())
		b, _ := json.Marshal(m["messages"])
		var msgs []struct {
			Content []map[string]any `json:"content"`
		}
		_ = json.Unmarshal(b, &msgs)
		for _, x := range msgs {
			for _, blk := range x.Content {
				if blk["type"] == "thinking" || blk["type"] == "redacted_thinking" {
					t.Fatal("translator não deve gerar blocos thinking")
				}
			}
		}
	})
	t.Run("cache_top_level_and_system_breakpoint", func(t *testing.T) {
		m := trq(t, `{"messages":[{"role":"system","content":"s"},{"role":"user","content":"a"}]}`, haikuOpts())
		jsonEq(t, m["cache_control"], `{"type":"ephemeral"}`)
		jsonEq(t, m["system"], `[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}]`)
	})
	t.Run("cache_off", func(t *testing.T) {
		o := haikuOpts()
		o.Cache = false
		m := trq(t, `{"messages":[{"role":"system","content":"s"},{"role":"user","content":"a"}]}`, o)
		if _, ok := m["cache_control"]; ok {
			t.Fatal("Cache=false não deveria marcar")
		}
		jsonEq(t, m["system"], `[{"type":"text","text":"s"}]`)
	})
	t.Run("disable_mode_not_continuation_keeps_effort", func(t *testing.T) {
		o := haikuOpts()
		o.ToolLoopMode = "disable"
		m := trq(t, `{"messages":[{"role":"user","content":"a"}],"max_tokens":1000}`, o)
		jsonEq(t, m["output_config"], `{"effort":"high"}`)
		if _, ok := m["thinking"]; ok {
			t.Fatal("disable só vale na continuação")
		}
	})
	t.Run("invalid_json_errors", func(t *testing.T) {
		if _, err := TranslateOpenAIToMessages([]byte(`{nope`), haikuOpts()); err == nil {
			t.Fatal("JSON inválido deveria dar erro")
		}
	})
	t.Run("missing_messages_errors", func(t *testing.T) {
		if _, err := TranslateOpenAIToMessages([]byte(`{"model":"x"}`), haikuOpts()); err == nil {
			t.Fatal("messages ausente deveria dar erro")
		}
	})
}

func TestIsToolLoopContinuation(t *testing.T) {
	tc := `{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"x","arguments":"{}"}}]}`
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"assistant_tool_calls_then_tools", `{"messages":[{"role":"user","content":"a"},` + tc + `,{"role":"tool","tool_call_id":"c1","content":"r"},{"role":"tool","tool_call_id":"c2","content":"r"}]}`, true},
		{"tool_then_user", `{"messages":[{"role":"user","content":"a"},` + tc + `,{"role":"tool","tool_call_id":"c1","content":"r"},{"role":"user","content":"b"}]}`, false},
		{"only_user", `{"messages":[{"role":"user","content":"a"}]}`, false},
		{"tool_calls_without_tool_after", `{"messages":[{"role":"user","content":"a"},` + tc + `]}`, false},
		{"last_assistant_without_tool_calls", `{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"tool","tool_call_id":"c1","content":"r"}]}`, false},
		{"invalid_json", `{nope`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsToolLoopContinuation([]byte(c.body)); got != c.want {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}
