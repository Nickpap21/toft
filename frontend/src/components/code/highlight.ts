// Syntax highlighting for the repository browser (D-73): Shiki on its
// JavaScript regex engine, so the panel's CSP needs no 'wasm-unsafe-eval',
// with each language a chunk of its own loaded the first time a file needs
// it, and colours from the theme's tokens (styles/code.css).
import type { HighlighterCore, ThemedToken } from "shiki/core";

type Loader = () => Promise<unknown>;

// The languages the browser highlights, by Shiki's name. Anything else is
// shown as plain text.
const LANGS: Record<string, Loader> = {
  go: () => import("shiki/langs/go.mjs"),
  typescript: () => import("shiki/langs/typescript.mjs"),
  tsx: () => import("shiki/langs/tsx.mjs"),
  javascript: () => import("shiki/langs/javascript.mjs"),
  jsx: () => import("shiki/langs/jsx.mjs"),
  json: () => import("shiki/langs/json.mjs"),
  yaml: () => import("shiki/langs/yaml.mjs"),
  toml: () => import("shiki/langs/toml.mjs"),
  markdown: () => import("shiki/langs/markdown.mjs"),
  shellscript: () => import("shiki/langs/shellscript.mjs"),
  docker: () => import("shiki/langs/docker.mjs"),
  python: () => import("shiki/langs/python.mjs"),
  php: () => import("shiki/langs/php.mjs"),
  ruby: () => import("shiki/langs/ruby.mjs"),
  rust: () => import("shiki/langs/rust.mjs"),
  java: () => import("shiki/langs/java.mjs"),
  kotlin: () => import("shiki/langs/kotlin.mjs"),
  c: () => import("shiki/langs/c.mjs"),
  cpp: () => import("shiki/langs/cpp.mjs"),
  csharp: () => import("shiki/langs/csharp.mjs"),
  swift: () => import("shiki/langs/swift.mjs"),
  css: () => import("shiki/langs/css.mjs"),
  scss: () => import("shiki/langs/scss.mjs"),
  html: () => import("shiki/langs/html.mjs"),
  xml: () => import("shiki/langs/xml.mjs"),
  sql: () => import("shiki/langs/sql.mjs"),
  ini: () => import("shiki/langs/ini.mjs"),
  make: () => import("shiki/langs/make.mjs"),
  nginx: () => import("shiki/langs/nginx.mjs"),
  vue: () => import("shiki/langs/vue.mjs"),
  svelte: () => import("shiki/langs/svelte.mjs"),
  hcl: () => import("shiki/langs/hcl.mjs"),
  graphql: () => import("shiki/langs/graphql.mjs"),
  proto: () => import("shiki/langs/proto.mjs"),
  diff: () => import("shiki/langs/diff.mjs"),
};

const EXT: Record<string, string> = {
  go: "go", ts: "typescript", mts: "typescript", cts: "typescript", tsx: "tsx",
  js: "javascript", mjs: "javascript", cjs: "javascript", jsx: "jsx",
  json: "json", jsonc: "json", yml: "yaml", yaml: "yaml", toml: "toml",
  md: "markdown", markdown: "markdown", sh: "shellscript", bash: "shellscript", zsh: "shellscript",
  py: "python", php: "php", rb: "ruby", rs: "rust", java: "java", kt: "kotlin", kts: "kotlin",
  c: "c", h: "c", cc: "cpp", cpp: "cpp", hpp: "cpp", cs: "csharp", swift: "swift",
  css: "css", scss: "scss", html: "html", htm: "html", xml: "xml", svg: "xml",
  sql: "sql", ini: "ini", cfg: "ini", conf: "ini", env: "ini", vue: "vue", svelte: "svelte",
  tf: "hcl", hcl: "hcl", graphql: "graphql", gql: "graphql", proto: "proto", diff: "diff", patch: "diff",
};

const NAMES: Record<string, string> = {
  dockerfile: "docker", containerfile: "docker", makefile: "make", gnumakefile: "make",
  "nginx.conf": "nginx", ".env": "ini", gemfile: "ruby", rakefile: "ruby",
};

/** Shiki's name for the language of a file called name, or null. */
export function languageFor(name: string): string | null {
  const base = name.split("/").pop()!.toLowerCase();
  if (NAMES[base]) return NAMES[base];
  if (base.startsWith("dockerfile.") || base.endsWith(".dockerfile")) return "docker";
  const dot = base.lastIndexOf(".");
  return dot > 0 || (dot === 0 && base.length > 1) ? EXT[base.slice(dot + 1)] ?? null : null;
}

// Highlighting stops here: past it a file shows as plain text, as quickly.
export const MAX_HIGHLIGHT = 300_000;

let highlighter: Promise<HighlighterCore> | null = null;

function core(): Promise<HighlighterCore> {
  highlighter ??= (async () => {
    const [{ createHighlighterCore, createCssVariablesTheme }, { createJavaScriptRegexEngine }] = await Promise.all([
      import("shiki/core"),
      import("shiki/engine/javascript"),
    ]);
    return createHighlighterCore({
      themes: [createCssVariablesTheme({ name: "toft", variablePrefix: "--shiki-", fontStyle: true })],
      langs: [],
      engine: createJavaScriptRegexEngine({ forgiving: true }),
    });
  })();
  return highlighter;
}

/** The lines of code as Shiki's tokens, or null when the language isn't one
 *  the browser highlights or the code is too long to. */
export async function tokenize(code: string, lang: string | null): Promise<ThemedToken[][] | null> {
  if (!lang || !LANGS[lang] || code.length > MAX_HIGHLIGHT) return null;
  const h = await core();
  if (!h.getLoadedLanguages().includes(lang)) {
    const mod = (await LANGS[lang]()) as { default: Parameters<HighlighterCore["loadLanguage"]>[0] };
    await h.loadLanguage(mod.default);
  }
  return h.codeToTokensBase(code, { lang, theme: "toft" });
}
