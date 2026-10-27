const {spawn, execFileSync} = require('node:child_process');
const fs = require('node:fs'); const path = require('node:path');
const {once} = require('node:events'); const readline = require('node:readline');
const root = path.resolve(__dirname,'../../..'); let child;
async function start(mode) {
 if(child) {child.stdin.end(); await once(child,'exit');}
 const bundle=mode==='before'?path.join(root,'.tmp/before/web/dist'):path.join(root,'web/dist');
 child=spawn(path.join(__dirname,mode+'.exe'),['--ui-dir',bundle],{cwd:root,stdio:['pipe','pipe','pipe'],windowsHide:true});
 let text=''; child.stdout.on('data',d=>{text+=d;}); child.stderr.on('data',d=>fs.appendFileSync(path.join(__dirname,mode+'-private.log'),d));
 let meta,url;
 for(let i=0;i<300;i++) {const m=text.match(/^journey_metadata:\s*(\{.*\})/m);url=text.match(/^api_url:\s*(\S+)/m)?.[1];if(m&&url){meta=JSON.parse(m[1]);break;}if(child.exitCode!==null)throw Error('server exited');await new Promise(r=>setTimeout(r,100));}
 if(!meta)throw Error('startup timed out');
 const repo=meta.workspace;
 let engine='';for(let i=0;i<5000;i++)engine+=`row ${String(i).padStart(4,'0')} ${'sample '.repeat(60)}\n`;
 let small='';for(let i=0;i<100;i++)small+=`export const value${i} = ${i};\n`;
 fs.writeFileSync(path.join(repo,'engine.txt'),engine);
 fs.writeFileSync(path.join(repo,'small.ts'),small);
 fs.writeFileSync(path.join(repo,'truncated.txt'),'old row\n'.repeat(40000));
 const git=args=>execFileSync('git',args,{cwd:repo,windowsHide:true,stdio:'pipe'});
 git(['init']);git(['add','.']);git(['-c','user.name=Diff acceptance','-c','user.email=diff@example.invalid','-c','core.hooksPath=NUL','commit','-m','Diff baseline']);
 fs.writeFileSync(path.join(repo,'engine.txt'),engine.replace('row 0010 ','changed 0010 ').replace('row 4990 ','changed 4990 '));
 fs.writeFileSync(path.join(repo,'small.ts'),small.replace('value10 = 10','value10 = 110').replace('value90 = 90','value90 = 190'));
 fs.writeFileSync(path.join(repo,'truncated.txt'),'new row\n'.repeat(40000));
 for(let i=0;i<55;i++)fs.writeFileSync(path.join(repo,`zz-page-${String(i).padStart(2,'0')}.txt`),'new file\n');
 const api=async(route,method='GET',body,key)=>{const r=await fetch(url+route,{method,headers:{Authorization:`Bearer ${method==='GET'?meta.read_token:meta.control_token}`,...(body?{'Content-Type':'application/json'}:{}),...(key?{'Idempotency-Key':key}:{})},...(body?{body:JSON.stringify(body)}:{})});const v=await r.json();if(!r.ok)throw Error(`${route}: ${r.status} ${JSON.stringify(v)}`);return v.data;};
 const imported=await api('/workspaces/import','POST',{version:'workspace_import.v1',directory_path:repo,confirmed:true,name:'Diff 大文件验收'});
 const made=await api('/threads','POST',{version:'thread_creation.v1',goal:'审查大文件中的两处修改与后续文件',workspace_id:imported.workspace.id,profile:'review',surface:'code',phase:'deliver'},'diff-scale-'+mode+'-20261027');
 await api(`/runs/${made.run.id}/lifecycle`,'POST',{version:'run_lifecycle_control.v1',action:'start'},'diff-scale-start-'+mode+'-20261027');
 const t=performance.now();const diff=await api(`/workspaces/${imported.workspace.id}/repository-diff`);const elapsed=performance.now()-t;
 fs.writeFileSync(path.join(__dirname,mode+'-observations.json'),JSON.stringify({mode,elapsed_ms:elapsed,source_bytes:Buffer.byteLength(engine),returned:diff.returned_count,omitted:diff.omitted_count,next_after:diff.next_after,total_patch_bytes:diff.total_patch_bytes,items:diff.items.slice(0,3).map(({path,content_state,statistics_complete,added_lines,deleted_lines,patch_bytes,truncated})=>({path,content_state,statistics_complete,added_lines,deleted_lines,patch_bytes,truncated}))},null,2));
 fs.writeFileSync(path.join(__dirname,'private-metadata.json'),JSON.stringify({...meta,apiURL:url,origin:new URL(url).origin,threadID:made.thread.id}));
 console.log(JSON.stringify({mode,origin:new URL(url).origin,threadID:made.thread.id}));
}
(async()=>{await start(process.argv[2] || 'before');for await(const line of readline.createInterface({input:process.stdin})){if(line==='after')await start('after');if(line==='stop')break;}})().catch(e=>{console.error(e);process.exitCode=1}).finally(async()=>{if(child&&child.exitCode===null){child.stdin.end();await once(child,'exit');}});
