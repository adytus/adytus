// Runs one Python program in Pyodide, for the API's python tool. The API
// starts this under Deno with no permissions but reading the Pyodide
// directory, so the program has no network, environment, subprocesses or
// host files, whatever it reaches through Pyodide's `js` module.
//
// It boots, then writes a line to stdout to say it is ready. Then it reads
// one job from stdin as JSON and writes the result to stdout as one line of
// JSON (see pyodide.go for both). Files the program wrote at the top of
// /work/out come back in base64; ones past the job's limits come back as a
// name and size only. Then it exits; a process is never reused.

// The Pyodide directory, as an absolute path, from deploy/pyodide/fetch.ts.
const dir = Deno.args[0];
const { loadPyodide } = await import(`file://${dir}/pyodide.mjs`);
// Bytes of stdout and stderr each that are kept; the API cuts them further.
const maxOut = 1 << 20;

const py = await loadPyodide({
  indexURL: `${dir}/`,
  env: { HOME: "/work", MPLBACKEND: "Agg", PYTHONDONTWRITEBYTECODE: "1" },
  stdout: () => {},
  stderr: () => {},
});
// Packages are extracted on disk and mounted, not unpacked into memory per
// run. Deno only grants reading, so code can't change them.
const FS = py.FS;
FS.mkdirTree("/opt/site-packages");
FS.mount(FS.filesystems.NODEFS, { root: `${dir}/site-packages` }, "/opt/site-packages");
// Fonts too, when this Pyodide directory was fetched with them.
let fonts = false;
try {
  fonts = Deno.statSync(`${dir}/fonts`).isDirectory;
} catch { /* fetched before fonts were added */ }
if (fonts) {
  FS.mkdirTree("/opt/fonts");
  FS.mount(FS.filesystems.NODEFS, { root: `${dir}/fonts` }, "/opt/fonts");
}
for (const d of ["/work/in", "/work/out"]) FS.mkdirTree(d);
py.runPython("import sys; sys.path.insert(0, '/opt/site-packages')");

const enc = new TextEncoder();
async function write(s) {
  const data = enc.encode(s);
  for (let n = 0; n < data.length;) n += await Deno.stdout.write(data.subarray(n));
}

// capture keeps the first maxOut bytes written to a stream.
function capture() {
  const parts = [];
  let size = 0;
  return {
    write(buf) {
      if (size < maxOut) {
        parts.push(buf.slice(0, maxOut - size));
        size += Math.min(buf.length, maxOut - size);
      }
      return buf.length;
    },
    text() {
      const all = new Uint8Array(size);
      let at = 0;
      for (const p of parts) { all.set(p, at); at += p.length; }
      return new TextDecoder().decode(all);
    },
  };
}

await write('{"ready":true}\n');
const job = JSON.parse(await new Response(Deno.stdin.readable).text());

for (const f of job.files ?? []) {
  FS.writeFile(`/work/in/${f.name}`, Uint8Array.fromBase64(f.data));
}

const stdout = capture();
const stderr = capture();
py.setStdout({ write: (b) => stdout.write(b) });
py.setStderr({ write: (b) => stderr.write(b) });

let exitCode = 0;
try {
  const run = py.runPython(`
import os, sys, traceback

def _adytus_run(src):
    os.chdir("/work")
    g = {"__name__": "__main__", "__builtins__": __builtins__}
    try:
        exec(compile(src, "<code>", "exec"), g)
    except SystemExit as e:
        if e.code is None:
            return 0
        if isinstance(e.code, int):
            return e.code
        print(e.code, file=sys.stderr)
        return 1
    except BaseException as e:
        traceback.print_exception(type(e), e, e.__traceback__.tb_next)
        return 1
    finally:
        sys.stdout.flush()
        sys.stderr.flush()
    return 0

_adytus_run
`);
  exitCode = run(job.code);
} catch (e) {
  stderr.write(enc.encode(String(e)));
  exitCode = 1;
}

const files = [];
let total = 0;
for (const name of FS.readdir("/work/out").sort()) {
  if (name.startsWith(".")) continue;
  const path = `/work/out/${name}`;
  const st = FS.lstat(path);
  if (!FS.isFile(st.mode)) continue;
  const fits = files.filter((f) => f.data).length < job.max_files &&
    st.size <= job.max_file_bytes && total + st.size <= job.max_total_bytes;
  if (!fits) {
    files.push({ name, size: st.size });
    continue;
  }
  total += st.size;
  files.push({ name, size: st.size, data: FS.readFile(path).toBase64() });
}
await write(JSON.stringify({ stdout: stdout.text(), stderr: stderr.text(), exit_code: exitCode, files }) + "\n");
Deno.exit(0);
