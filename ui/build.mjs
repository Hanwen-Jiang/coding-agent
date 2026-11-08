import {build} from 'esbuild';
import {readFile,writeFile} from 'node:fs/promises';
import {resolve,dirname} from 'node:path';
import {fileURLToPath} from 'node:url';
const here=dirname(fileURLToPath(import.meta.url));
const modules=resolve(process.argv[2] || resolve(here,'node_modules'));
for(const [name,version] of Object.entries({'@heroui-pro/react':'1.0.0-beta.11','@heroui/react':'3.2.7',react:'19.3.1','react-dom':'19.3.1',shiki:'4.6.0'})) {
 const installed=JSON.parse(await readFile(resolve(modules,name,'package.json'),'utf8'));
 if(installed.version!==version)throw Error(`${name}: expected ${version}, got ${installed.version}`);
}
const out=resolve(here,'../internal/web/static/chat-ui.js');
const result=await build({entryPoints:[resolve(here,'chat/index.tsx')],outfile:out,bundle:true,minify:true,format:'esm',target:'es2022',jsx:'automatic',nodePaths:[modules,resolve(here,"node_modules")],alias:{shiki:resolve(here,'chat/highlight.ts')},define:{'process.env.NODE_ENV':'"production"'},metafile:true,legalComments:'eof'});
const components=['chat-message','markdown','code-block'];
const css=await Promise.all(components.map(name=>readFile(resolve(modules,'@heroui-pro/react/dist/css/components',name+'.css'),'utf8')));
await writeFile(resolve(here,'../internal/web/static/chat-ui.css'),'/* HeroUI Pro 1.0.0-beta.11: upstream component CSS scoped to React islands. */\n@scope (.pro-chat) {\n'+css.join('\n')+'\n}\n');
await writeFile(resolve(here,'build-meta.json'),JSON.stringify({packages:{'@heroui-pro/react':'1.0.0-beta.11',react:'19.3.1',shiki:'4.6.0'},output_bytes:result.metafile.outputs[out]?.bytes || (await readFile(out)).length,components},null,2)+'\n');
console.log(`Built ${out}: ${(await readFile(out)).length} bytes`);
