package slacktext

import (
	"strings"
	"testing"
)

func TestHTMLToMarkdown(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{
			"basic document",
			`<html><body><h1>Title</h1><p>Hello <a href="https://example.com">world</a>.</p></body></html>`,
			"# Title\n\nHello [world](https://example.com).",
		},
		{
			"prefers <main> over the rest of the body",
			`<html><head><title>T</title><style>p{}</style></head><body><nav>menu</nav><main class="x"><h2>Main</h2><p>text</p></main><footer>foot</footer></body></html>`,
			"## Main\n\ntext",
		},
		{
			"prefers <article> when there is no <main>",
			`<body><nav>menu</nav><ARTICLE><p>text</p></ARTICLE></body>`,
			"text",
		},
		{"fragment", `<p>just a <em>fragment</em></p>`, "just a _fragment_"},
		{"line breaks", `<p>line one<br>line two<br/>line three</p>`, "line one\nline two\nline three"},
		{"inline styles", `<p><strong>bold</strong> <i>it</i> <del>gone</del> <s>too</s> <code>x_y</code></p>`, "**bold** _it_ ~~gone~~ ~~too~~ `x_y`"},
		{
			"lists",
			`<ul><li>a</li><li>b<ul><li>c</li></ul></li></ul><ol><li>x</li><li>y</li></ol>`,
			"- a\n- b\n\n  - c\n\n1. x\n2. y",
		},
		{
			"task list",
			`<ul><li><input type="checkbox" checked> done</li><li><input type="checkbox">todo</li></ul>`,
			"- [x] done\n- [ ] todo",
		},
		{
			"fenced code block",
			"<pre><code class=\"language-go\">func main() {\n\tx := 1 \n}\n</code></pre>",
			"```go\nfunc main() {\n\tx := 1 \n}\n```",
		},
		{"blockquote and rule", `<blockquote><p>quoted</p></blockquote><hr>`, "> quoted\n\n* * *"},
		{
			"table with a header",
			`<table><thead><tr><th>h1</th><th>h2</th></tr></thead><tbody><tr><td>1</td><td>2</td></tr></tbody></table>`,
			"| h1 | h2 |\n|---|---|\n| 1 | 2 |",
		},
		{
			"table without a header",
			`<table><tr><td>a</td><td>b</td></tr><tr><td>1</td><td>2</td></tr></table>`,
			"| a | b |\n|---|---|\n| 1 | 2 |",
		},
		{"image", `<p><img src="https://example.com/y.png" alt="pic"></p>`, "![pic](https://example.com/y.png)"},
		{"markdown characters are escaped", `<p>snake_case and 2*3</p>`, `snake\_case and 2\*3`},
		{"scripts are dropped", `<p>a</p><script>alert(1)</script><style>p{}</style>`, "a"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertEqual(t, strings.TrimSpace(HTMLToMarkdown(tt.in)), tt.want)
		})
	}
}
