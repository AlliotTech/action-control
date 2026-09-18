#!/usr/bin/env node
import { createHash } from 'node:crypto';
import { createReadStream } from 'node:fs';
import { mkdir, writeFile, copyFile, stat, rename, rm, chmod } from 'node:fs/promises';
import { spawn } from 'node:child_process';
import { dirname, resolve, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { availableParallelism, platform, arch } from 'node:os';

const root=resolve(dirname(fileURLToPath(import.meta.url)),'..');
const cache=join(root,'.build-tools');
const host=`${platform()}-${arch()}`;
const zigHosts={
 'darwin-arm64':{name:'zig-aarch64-macos-0.16.0.tar.xz',sha256:'b23d70deaa879b5c2d486ed3316f7eaa53e84acf6fc9cc747de152450d401489'},
 'darwin-x64':{name:'zig-x86_64-macos-0.16.0.tar.xz',sha256:'0387557ed1877bc6a2e1802c8391953baddba76081876301c522f52977b52ba7'},
 'linux-arm64':{name:'zig-aarch64-linux-0.16.0.tar.xz',sha256:'ea4b09bfb22ec6f6c6ceac57ab63efb6b46e17ab08d21f69f3a48b38e1534f17'},
 'linux-x64':{name:'zig-x86_64-linux-0.16.0.tar.xz',sha256:'70e49664a74374b48b51e6f3fdfbf437f6395d42509050588bd49abe52ba3d00'},
};
if(!zigHosts[host])throw new Error(`Native media build needs a supported Unix host, got ${host}`);
const packages=[
 {name:'ffmpeg-9.0.1.tar.xz',url:'https://ffmpeg.org/releases/ffmpeg-9.0.1.tar.xz',sha256:'cf38e0e28c7e5605942c4a77755349b0145804a397af37eb1fb4c77cb237f635',dir:'ffmpeg-9.0.1'},
 {name:'zlib-1.3.2.tar.xz',url:'https://zlib.net/zlib-1.3.2.tar.xz',sha256:'d7a0654783a4da529d1bb793b7ad9c3318020af77667bcae35f95d0e42a792f3',dir:'zlib-1.3.2'},
 {name:'sqlite-autoconf-3510100.tar.gz',url:'https://www.sqlite.org/2025/sqlite-autoconf-3510100.tar.gz',sha256:'4f2445cd70479724d32ad015ec7fd37fbb6f6130013bd4bfbc80c32beb42b7e0',dir:'sqlite-autoconf-3510100'},
 {...zigHosts[host],url:`https://ziglang.org/download/0.16.0/${zigHosts[host].name}`,dir:'zig-0.16.0'},
];
async function digest(file){const hash=createHash('sha256');for await(const data of createReadStream(file))hash.update(data);return hash.digest('hex')}
async function run(program,args,cwd,env={}){const inherited=Object.fromEntries(['PATH','HOME','TMPDIR','LANG','http_proxy','https_proxy','ALL_PROXY','NO_PROXY'].filter(key=>process.env[key]!==undefined).map(key=>[key,process.env[key]]));await new Promise((resolve,reject)=>{const child=spawn(program,args,{cwd,env:{...inherited,...env},stdio:'inherit'});child.on('error',reject);child.on('exit',(code,signal)=>code===0?resolve():reject(new Error(`${program} exited ${code??signal}`)))})}
await mkdir(join(cache,'downloads'),{recursive:true});
for(const pkg of packages){
 const archive=join(cache,'downloads',pkg.name);
 try{await stat(archive)}catch(error){if(error.code!=='ENOENT')throw error;await run('curl',['--fail','--location','--output',archive,pkg.url],root)}
 if(await digest(archive)!==pkg.sha256)throw new Error(`Checksum mismatch: ${pkg.name}`);
 const output=join(cache,pkg.dir);
 try{await stat(join(output,'.extracted'))}catch(error){if(error.code!=='ENOENT')throw error;const staging=`${output}.extracting`;await rm(staging,{force:true,recursive:true});await mkdir(staging,{recursive:true});await run('tar',['-xf',archive,'--strip-components=1','-C',staging],root);await writeFile(join(staging,'.extracted'),pkg.sha256+'\n');await rm(output,{force:true,recursive:true});await rename(staging,output)}
}
const zig=join(cache,'zig-0.16.0','zig');
const cc=join(cache,'cc'),cxx=join(cache,'cxx');
await writeFile(cc,'#!/bin/sh\nexec "$ACTION_ZIG" cc -target aarch64-linux-musl "$@"\n',{mode:0o755});
await writeFile(cxx,'#!/bin/sh\nexec "$ACTION_ZIG" c++ -target aarch64-linux-musl "$@"\n',{mode:0o755});
const env={ACTION_ZIG:zig,CC:cc,CXX:cxx,AR:`${zig} ar`,RANLIB:`${zig} ranlib`,CFLAGS:'-O2',LDFLAGS:'-static'};
const prefix=join(cache,'target');await mkdir(prefix,{recursive:true});
const jobs=String(Math.min(8,availableParallelism()));
await run('./configure',['--static','--uname=Linux',`--prefix=${prefix}`],join(cache,'zlib-1.3.2'),env);
await run('make',['clean'],join(cache,'zlib-1.3.2'),env);
await run('make',['-j',jobs,'install'],join(cache,'zlib-1.3.2'),env);
await run(cc,['-O2','-static','-DSQLITE_THREADSAFE=1','-DSQLITE_OMIT_LOAD_EXTENSION','-o',join(cache,'sqlite3'),join(cache,'sqlite-autoconf-3510100','shell.c'),join(cache,'sqlite-autoconf-3510100','sqlite3.c'),'-lpthread','-lm','-ldl'],root,env);
const flags=[`--prefix=${prefix}`,'--arch=aarch64','--target-os=linux','--enable-cross-compile',`--cc=${cc}`,`--cxx=${cxx}`,`--ld=${cc}`,`--ar=${zig} ar`,`--ranlib=${zig} ranlib`,'--pkg-config=false','--disable-autodetect','--disable-shared','--enable-static','--disable-debug','--disable-doc','--disable-ffplay','--disable-avdevice','--disable-stripping','--enable-zlib',`--extra-cflags=-I${prefix}/include`,`--extra-ldflags=-L${prefix}/lib -static -s`];
await run('./configure',flags,join(cache,'ffmpeg-9.0.1'),env);
await run('make',['-j',jobs,'ffmpeg','ffprobe'],join(cache,'ffmpeg-9.0.1'),env);
const media=join(root,'media');for(const name of ['bin','licenses','sources'])await mkdir(join(media,name),{recursive:true});
for(const name of ['ffmpeg','ffprobe']){await copyFile(join(cache,'ffmpeg-9.0.1',name),join(media,'bin',name));await chmod(join(media,'bin',name),0o755)}
await copyFile(join(cache,'sqlite3'),join(media,'bin','sqlite3'));await chmod(join(media,'bin','sqlite3'),0o755);
for(const name of ['COPYING.LGPLv2.1','COPYING.LGPLv3','COPYING.GPLv2','COPYING.GPLv3'])await copyFile(join(cache,'ffmpeg-9.0.1',name),join(media,'licenses',`${name}.txt`));
await copyFile(join(cache,'zlib-1.3.2','LICENSE'),join(media,'licenses','zlib.txt'));
for(const pkg of packages.slice(0,3))await copyFile(join(cache,'downloads',pkg.name),join(media,'sources',pkg.name));
const hashes={};for(const name of ['ffmpeg','ffprobe','sqlite3'])hashes[name]=await digest(join(media,'bin',name));
const portableFlags=flags.map(flag=>flag.replaceAll(root,'$PROJECT_ROOT'));
await writeFile(join(media,'provenance.json'),JSON.stringify({ffmpeg:'9.0.1',zlib:'1.3.2',sqlite:'3.51.1',zig:'0.16.0',target:'aarch64-linux-musl',source:packages,configure:portableFlags,binaries:hashes,license:'FFmpeg LGPL-2.1-or-later, zlib Zlib, SQLite public domain; unmodified corresponding sources included. Checksums ensure integrity, not publisher identity.'},null,2)+'\n');
console.log('Built pinned FFmpeg 9.0.1, ffprobe, and SQLite 3.51.1 for Linux ARM64 in media/bin');
