#!/usr/bin/env node
import { createHash } from "node:crypto";
import { createReadStream } from "node:fs";
import {
  cp,
  chmod,
  copyFile,
  lstat,
  mkdir,
  mkdtemp,
  open,
  readFile,
  readdir,
  rename,
  rm,
  writeFile,
} from "node:fs/promises";
import { execFileSync } from "node:child_process";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const web = join(root, "web");
const pkg = JSON.parse(await readFile(join(web, "package.json"), "utf8"));
if (!/^\d+\.\d+\.\d+(?:-[a-zA-Z0-9.-]+)?$/.test(pkg.version))
  throw new Error("Invalid release version");
const goVersion = (await readFile(join(root, "go.mod"), "utf8")).match(
  /^go (\S+)$/m,
)?.[1];
const run = (program, args, options = {}) =>
  execFileSync(program, args, { cwd: root, stdio: "inherit", ...options });
const capture = (program, args, options = {}) =>
  run(program, args, {
    stdio: ["ignore", "pipe", "inherit"],
    encoding: "utf8",
    ...options,
  }).trim();
if (!capture("go", ["version"]).includes(` go${goVersion} `))
  throw new Error(`Select pinned Go ${goVersion} before building`);
const expectedNode = pkg.engines.node.replace(">=", "");
if (process.versions.node !== expectedNode)
  throw new Error(`Select pinned Node ${expectedNode} before building`);

async function sha256(file) {
  const hash = createHash("sha256");
  for await (const bytes of createReadStream(file)) hash.update(bytes);
  return hash.digest("hex");
}
async function verifyELF(file) {
  const fd = await open(file, "r");
  try {
    const read = async (size, offset) => {
      const buffer = Buffer.alloc(size);
      if ((await fd.read(buffer, 0, size, offset)).bytesRead !== size)
        throw new Error(`Truncated ELF: ${file}`);
      return buffer;
    };
    const header = await read(64, 0);
    if (
      !header.subarray(0, 6).equals(Buffer.from([127, 69, 76, 70, 2, 1])) ||
      header.readUInt16LE(18) !== 183 ||
      ![2, 3].includes(header.readUInt16LE(16))
    )
      throw new Error(`Not a little-endian ARM64 executable: ${file}`);
    const offset = Number(header.readBigUInt64LE(32)),
      count = header.readUInt16LE(56),
      stride = header.readUInt16LE(54);
    if (!Number.isSafeInteger(offset) || count === 0 || stride < 56)
      throw new Error(`Invalid ELF program headers: ${file}`);
    for (let i = 0; i < count; i++) {
      const entry = await read(56, offset + i * stride),
        type = entry.readUInt32LE(0);
      if (type === 3) throw new Error(`Dynamic interpreter found: ${file}`);
      if (type === 2) {
        const start = Number(entry.readBigUInt64LE(8)),
          length = Number(entry.readBigUInt64LE(32));
        if (!Number.isSafeInteger(start + length))
          throw new Error("Invalid dynamic segment");
        for (let j = 0; j < length; j += 16) {
          const tag = (await read(16, start + j)).readBigInt64LE(0);
          if (tag === 0n) break;
          if (tag === 1n)
            throw new Error(`Dynamic library dependency found: ${file}`);
        }
      }
    }
  } finally {
    await fd.close();
  }
}
const media = join(root, "media");
const provenance = JSON.parse(
  await readFile(join(media, "provenance.json"), "utf8"),
);
for (const name of ["ffmpeg", "ffprobe", "sqlite3"]) {
  const file = join(media, "bin", name);
  await verifyELF(file);
  if ((await sha256(file)) !== provenance.binaries[name])
    throw new Error(`Media tool provenance mismatch: ${name}`);
}
for (const source of provenance.source.filter(
  (item) => !item.name.startsWith("zig-"),
)) {
  if ((await sha256(join(media, "sources", source.name))) !== source.sha256)
    throw new Error(`Corresponding source mismatch: ${source.name}`);
}
run("npm", ["--prefix", web, "ci"]);
run("npm", ["--prefix", web, "run", "build"]);
await rm(join(root, "cmd/action-control/web/dist"), { force: true, recursive: true });
await cp(join(web, "dist"), join(root, "cmd/action-control/web/dist"), {
  recursive: true,
});
const index = await readFile(join(web, "dist", "index.html"), "utf8");
for (const match of index.matchAll(/(?:src|href)="(\/[^"?#]+)"/g)) {
  const asset = await lstat(join(web, "dist", match[1]));
  if (!asset.isFile()) throw new Error(`Missing frontend asset: ${match[1]}`);
}
if (!index.includes('type="module"'))
  throw new Error("Frontend build entry is missing");

const release = join(root, "release"),
  name = `action-control-${pkg.version}-linux-arm64`;
await mkdir(release, { recursive: true });
const destination = join(release, name);
try {
  await lstat(destination);
  throw new Error(`Release directory already exists: ${destination}`);
} catch (e) {
  if (e.code !== "ENOENT") throw e;
}
const staging = await mkdtemp(join(release, ".action-control-build-"));
try {
  const payload = join(staging, "payload"),
    licenses = join(payload, "licenses");
  await mkdir(join(payload, "bin"), { recursive: true });
  await mkdir(licenses);
  await copyFile(join(root, "LICENSE"), join(licenses, "Action-Control-LICENSE.txt"));
  run(
    "go",
    [
      "build",
      "-trimpath",
      "-ldflags",
      `-s -w -X main.Version=${pkg.version}`,
      "-o",
      join(payload, "action-control"),
      "./cmd/action-control",
    ],
    {
      env: { ...process.env, GOOS: "linux", GOARCH: "arm64", CGO_ENABLED: "0" },
    },
  );
  await verifyELF(join(payload, "action-control"));
  for (const tool of ["ffmpeg", "ffprobe", "sqlite3"])
    await copyFile(join(media, "bin", tool), join(payload, "bin", tool));
  for (const entry of await readdir(join(media, "licenses"), {
    withFileTypes: true,
  })) {
    if (!entry.isFile())
      throw new Error("Media licenses must be flat regular files");
    await copyFile(
      join(media, "licenses", entry.name),
      join(licenses, entry.name),
    );
  }
  const dependencies = new Set(
    capture("npm", [
      "--prefix",
      web,
      "ls",
      "--omit=dev",
      "--all",
      "--parseable",
    ])
      .split(/\r?\n/)
      .slice(1),
  );
  dependencies.add(join(web, "node_modules", "tailwindcss"));
  for (const directory of dependencies) {
    const dependency = JSON.parse(
      await readFile(join(directory, "package.json"), "utf8"),
    );
    const names = (await readdir(directory)).filter((name) =>
      /^(?:licen[sc]e|copying|notice)(?:[._-].*)?$/i.test(name),
    );
    if (
      !names.length &&
      !(
        await lstat(
          join(
            licenses,
            `${dependency.name.replaceAll("/", "__")}@${dependency.version}-LICENSE.txt`,
          ),
        )
      ).isFile()
    )
      throw new Error(`Missing dependency license: ${dependency.name}`);
    for (const license of names) {
      const file = join(directory, license);
      if (!(await lstat(file)).isFile()) continue;
      await copyFile(
        file,
        join(
          licenses,
          `${dependency.name.replaceAll("/", "__")}@${dependency.version}-${license}`,
        ),
      );
    }
  }
  await copyFile(
    join(capture("go", ["env", "GOROOT"]), "LICENSE"),
    join(licenses, "Go-LICENSE.txt"),
  );
  const sys = JSON.parse(
    capture("go", ["list", "-m", "-json", "golang.org/x/sys"]),
  );
  await copyFile(
    join(sys.Dir, "LICENSE"),
    join(licenses, "golang.org-x-sys-LICENSE.txt"),
  );
  await copyFile(
    join(media, "provenance.json"),
    join(licenses, "media-provenance.json"),
  );
  const files = [];
  for (const directory of ["", "bin", "licenses"]) {
    for (const entry of await readdir(join(payload, directory), {
      withFileTypes: true,
    })) {
      if (entry.isDirectory() && directory === "") continue;
      if (!entry.isFile())
        throw new Error(
          "Payload cannot contain symlinks or nested directories",
        );
      const path = directory ? `${directory}/${entry.name}` : entry.name,
        file = join(payload, path);
      const mode = directory === "licenses" ? 0o644 : 0o755;
      await chmod(file, mode);
      files.push({
        path,
        sha256: await sha256(file),
        size: (await lstat(file)).size,
        mode,
      });
    }
  }
  files.sort((a, b) => a.path.localeCompare(b.path));
  await writeFile(
    join(payload, "manifest.json"),
    JSON.stringify({ schema: 1, version: pkg.version, files }, null, 2) + "\n",
  );
  for (const script of [
    "install.sh",
    "uninstall.sh",
    "install.ps1",
    "uninstall.ps1",
  ]) {
    await copyFile(join(root, script), join(staging, script));
    await chmod(join(staging, script), script.endsWith(".sh") ? 0o755 : 0o644);
  }
  await mkdir(join(staging, "sources"));
  for (const entry of await readdir(join(media, "sources"), {
    withFileTypes: true,
  })) {
    if (!entry.isFile())
      throw new Error("Expected regular corresponding source archives");
    await copyFile(
      join(media, "sources", entry.name),
      join(staging, "sources", entry.name),
    );
  }
  await copyFile(
    join(root, "tools", "build-ffmpeg.mjs"),
    join(staging, "sources", "build-ffmpeg.mjs"),
  );
  await copyFile(
    join(media, "provenance.json"),
    join(staging, "sources", "provenance.json"),
  );
  const sums = [];
  async function checksumTree(directory) {
    for (const entry of (
      await readdir(directory, { withFileTypes: true })
    ).sort((a, b) => a.name.localeCompare(b.name))) {
      const file = join(directory, entry.name);
      if (entry.isDirectory()) await checksumTree(file);
      else if (entry.isFile())
        sums.push(
          `${await sha256(file)}  ${relative(staging, file).replaceAll("\\", "/")}`,
        );
      else throw new Error("Release contains an unexpected symlink");
    }
  }
  await checksumTree(staging);
  await writeFile(join(staging, "SHA256SUMS"), sums.join("\n") + "\n");
  await rename(staging, destination);
} catch (e) {
  await rm(staging, { recursive: true, force: true });
  throw e;
}
const archive = `${destination}.tar.gz`;
run("tar", ["-czf", archive, "-C", release, name]);
await writeFile(
  `${archive}.sha256`,
  `${await sha256(archive)}  ${name}.tar.gz\n`,
);
console.log(
  `Release: ${archive}\nChecksums verify transfer integrity, not publisher identity.`,
);
