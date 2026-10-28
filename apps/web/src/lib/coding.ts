// Whether a question is about code. The tool line names Python only when it
// is; otherwise the step is just the model working something out and reads
// "Calculating", with the code kept folded away.

const languages = [
	'python',
	'javascript',
	'typescript',
	'nodejs',
	'java',
	'golang',
	'ruby',
	'php',
	'kotlin',
	'scala',
	'haskell',
	'perl',
	'lua',
	'elixir',
	'bash',
	'powershell',
	'sql',
	'mysql',
	'postgres',
	'postgresql',
	'sqlite',
	'mongodb',
	'graphql',
	'html',
	'css',
	'scss',
	'sass',
	'svelte',
	'vue',
	'django',
	'flask',
	'fastapi'
];

const terms = [
	'code',
	'coding',
	'coder',
	'program',
	'programming',
	'script',
	'function',
	'algorithm',
	'api',
	'endpoint',
	'compile',
	'compiler',
	'debug',
	'refactor',
	'snippet',
	'syntax',
	'stack ?trace',
	'traceback',
	'unit test',
	'pytest',
	'regex',
	'regexp',
	'git',
	'docker',
	'npm',
	'pip install',
	'virtualenv'
];

const language = new RegExp(`\\b(?:${languages.join('|')})\\b`);
const term = new RegExp(`\\b(?:${terms.join('|')})\\b`);
// A filename with a code extension.
const file = /[\w-]+\.(?:py|pyw|js|mjs|cjs|ts|tsx|jsx|go|rs|java|rb|php|c|h|cpp|hpp|cs|html|css|scss|sql|sh|ps1)\b/;
// A call such as f(x) or printf(, or a snake_case or camelCase name. Calls
// start with a letter so sums like "3(x+1)" aren't mistaken for code.
const call = /[A-Za-z_]\w*\(|\b[a-zA-Z]\w*_[a-zA-Z]\w*|\b[a-z]{2,}[A-Z][a-z]+\w*/;
// Source lines pasted without a fence. "import duties" is prose, so an
// import only counts on a line of its own.
const pasted =
	/^\s*(?:def \w+\(|class \w+[:(]|(?:const|let|var) \w+ ?=|function \w+\(|#include\s*<?|import [\w.]+$|from [\w.]+ import )/m;

export function isCodingQuery(text: string): boolean {
	if (/```|~~~/.test(text)) return true;
	if (pasted.test(text) || file.test(text) || call.test(text)) return true;
	if (/\bc(?:\+\+|#)/i.test(text)) return true;
	const t = text.toLowerCase();
	return language.test(t) || term.test(t);
}
