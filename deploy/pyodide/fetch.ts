// Downloads the Python sandbox's runtime: Pyodide (CPython built for
// WebAssembly) and the packages code can import, into one directory that the
// API's Deno runner reads at run time. Packages are extracted to
// <dir>/site-packages, which the runner mounts read-only; the downloads stay
// in <dir>/cache so a rerun only fetches what changed. Nothing is fetched at
// run time, and every file is checked against a pinned SHA-256. Run from the
// repo root:
//
//   deno run --allow-net --allow-read --allow-write deploy/pyodide/fetch.ts apps/api/.pyodide
//
// To upgrade Pyodide, change VERSION and CORE: the hashes are those of the
// files in the release's pyodide-core tarball. Lock packages are checked
// against the hashes in its pyodide-lock.json.

const VERSION = "314.0.7";
const CDN = `https://cdn.jsdelivr.net/pyodide/v${VERSION}/full/`;

const CORE: Record<string, string> = {
  "pyodide.mjs": "6f1d60f7bf529beb300f0f47983c921d3982363640ba20af0e38efdddbc66109",
  "pyodide.asm.mjs": "f7cdc8ece80678ceb712f8e65ebe6d3a83203a180c399865f49612a051693635",
  "pyodide.asm.wasm": "cc36e3cab04fdfc9a63ff13eb52eae2b911bf46c025cc7b281f394bd3de1d5e6",
  "python_stdlib.zip": "fa1957e5777068fc4f7437f96d860ae2fbe9c19732ba06c84e004ec16dd7dd7a",
  "pyodide-lock.json": "5dc2fc119108bc148c7457dc86e7675b5c87e1cafd420b9c34c1eaef7b36c010",
};

// Built by Pyodide; their dependencies come along.
const PACKAGES = [
  "numpy", "scipy", "pandas", "matplotlib", "pillow", "lxml",
  "beautifulsoup4", "xlrd", "sympy", "pyyaml", "typing-extensions",
  "cryptography", "charset-normalizer", "fonttools",
];

// Pure-Python wheels from PyPI, for working with documents. Their
// dependencies are in PACKAGES.
const WHEELS: Record<string, string> = {
  "https://files.pythonhosted.org/packages/py3/p/python-docx/python_docx-1.2.0-py3-none-any.whl":
    "3fd478f3250fbbbfd3b94fe1e985955737c145627498896a8a6bf81f4baf66c7",
  "https://files.pythonhosted.org/packages/py3/p/python-pptx/python_pptx-1.0.2-py3-none-any.whl":
    "160838e0b8565a8b1f67947675886e9fea18aa5e795db7ae531606d68e785cba",
  "https://files.pythonhosted.org/packages/py2.py3/o/openpyxl/openpyxl-3.1.5-py2.py3-none-any.whl":
    "5282c12b107bffeef825f4617dc029afaf41d0ea60823bbb665ef3079dc79de2",
  "https://files.pythonhosted.org/packages/py3/e/et-xmlfile/et_xmlfile-2.0.0-py3-none-any.whl":
    "7a91720bc756843502c3b7504c77b8fe44217c85c537d85037f0f536151b2caa",
  "https://files.pythonhosted.org/packages/py3/x/xlsxwriter/xlsxwriter-3.2.9-py3-none-any.whl":
    "9a5db42bc5dff014806c58a20b9eae7322a134abb6fce3c92c181bfb275ec5b3",
  "https://files.pythonhosted.org/packages/py3/p/pypdf/pypdf-6.19.0-py3-none-any.whl":
    "7e5d6e730e7dae87d560a2cee218b852f6498c8be61966f3cd02ead971e48d14",
  "https://files.pythonhosted.org/packages/py3/p/pdfminer.six/pdfminer_six-20260107-py3-none-any.whl":
    "366585ba97e80dffa8f00cebe303d2f381884d8637af4ce422f1df3ef38111a9",
  "https://files.pythonhosted.org/packages/py3/f/fpdf2/fpdf2-2.8.9-py3-none-any.whl":
    "6e1d94af6d6311950a23dec7fb5fc84b000203eb59aee8e76c1e701b12a14976",
  "https://files.pythonhosted.org/packages/py2.py3/d/defusedxml/defusedxml-0.7.1-py2.py3-none-any.whl":
    "a352e7e428770286cc899e2542b6cdaedb2b4953ff269a210103ec58f6198a61",
  "https://files.pythonhosted.org/packages/py3/t/tabulate/tabulate-0.10.0-py3-none-any.whl":
    "f0b0622e567335c8fabaaa659f1b33bcb6ddfe2e496071b743aa113f8774f2d3",
};

type Lock = { packages: Record<string, { file_name: string; sha256: string; depends: string[] }> };

const out = Deno.args[0];
if (!out) {
  console.error("usage: fetch.ts <dir>");
  Deno.exit(2);
}
await Deno.mkdir(out, { recursive: true });
const dir = await Deno.realPath(out);
const cache = `${dir}/cache`;
await Deno.mkdir(cache, { recursive: true });

async function sha256(data: Uint8Array): Promise<string> {
  const d = new Uint8Array(await crypto.subtle.digest("SHA-256", data));
  return Array.from(d, (b) => b.toString(16).padStart(2, "0")).join("");
}

// fetchTo saves url at path unless a file with the right hash is there.
async function fetchTo(url: string, path: string, want: string) {
  try {
    if (await sha256(await Deno.readFile(path)) === want) return;
  } catch { /* not there yet */ }
  const res = await fetch(url);
  if (!res.ok) throw new Error(`${url}: ${res.status}`);
  const data = new Uint8Array(await res.arrayBuffer());
  const got = await sha256(data);
  if (got !== want) throw new Error(`${url}: sha256 ${got}, want ${want}`);
  await Deno.writeFile(path, data);
  console.log("fetched", path);
}

for (const [name, hash] of Object.entries(CORE)) {
  await fetchTo(CDN + name, `${dir}/${name}`, hash);
}

const lock: Lock = JSON.parse(await Deno.readTextFile(`${dir}/pyodide-lock.json`));
const need = new Set<string>();
const walk = (name: string) => {
  const p = lock.packages[name];
  if (!p) throw new Error(`${name} is not in pyodide-lock.json`);
  if (need.has(name)) return;
  need.add(name);
  p.depends.forEach(walk);
};
PACKAGES.forEach(walk);
const wheels: string[] = [];
for (const name of need) {
  const p = lock.packages[name];
  await fetchTo(CDN + p.file_name, `${cache}/${p.file_name}`, p.sha256);
  wheels.push(p.file_name);
}
for (const [url, hash] of Object.entries(WHEELS)) {
  const file = url.slice(url.lastIndexOf("/") + 1);
  await fetchTo(url, `${cache}/${file}`, hash);
  wheels.push(file);
}

// Pyodide itself unpacks the wheels, through a mount of site-packages.
const site = `${dir}/site-packages`;
await Deno.remove(site, { recursive: true }).catch(() => {});
await Deno.mkdir(site);
const { loadPyodide } = await import(`file://${dir}/pyodide.mjs`);
const py = await loadPyodide({ indexURL: `${dir}/` });
py.FS.mkdirTree("/site");
py.FS.mount(py.FS.filesystems.NODEFS, { root: site }, "/site");
for (const w of wheels) {
  py.unpackArchive(await Deno.readFile(`${cache}/${w}`), "wheel", { extractDir: "/site" });
}

// The API checks this is here, and names the version in its logs.
await Deno.writeTextFile(`${dir}/adytus.json`, JSON.stringify({ version: VERSION, wheels: wheels.sort() }, null, 2) + "\n");
console.log(`pyodide ${VERSION}: ${wheels.length} packages in ${dir}`);
