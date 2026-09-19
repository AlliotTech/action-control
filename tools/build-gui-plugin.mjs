#!/usr/bin/env node
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root=resolve(dirname(fileURLToPath(import.meta.url)),'..');
const zig=process.env.ACTION_GUI_ZIG || join(root,'.build-tools/zig-0.16.0/zig');
const version=execFileSync(zig,['version'],{encoding:'utf8'}).trim();
if(version!=='0.16.0')throw new Error('GUI plugin requires Zig 0.16.0');
const output=join(root,'.build-tools/gui-plugin');
await mkdir(output,{recursive:true});
const library=join(output,'libaction_control_gui.so');
execFileSync(zig,['cc','-target','aarch64-linux-gnu.2.35','-std=c11','-O2','-fPIC','-shared','-fvisibility=hidden','-mbranch-protection=standard','-Wall','-Wextra','-Werror','-Wl,-z,relro,-z,now,-z,nodelete','-Wl,--no-undefined','-o',library,...['plugin.c','sha256.c','ui.c'].map(p=>join(root,'native/gui',p)),'-ldl','-pthread'],{stdio:'inherit'});
const data=await readFile(library);
if(data.readUInt16LE(18)!==183 || data.readUInt16LE(16)!==3)throw new Error('Expected AArch64 shared library');
const manifest={file:'libaction_control_gui.so',sha256:createHash('sha256').update(data).digest('hex'),size:data.length,target:'aarch64-linux-gnu.2.35',zig:version,prototype:true};
await writeFile(join(output,'manifest.json'),JSON.stringify(manifest,null,2)+'\n');
console.log(JSON.stringify(manifest));
