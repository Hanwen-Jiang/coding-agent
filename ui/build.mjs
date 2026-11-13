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
const result=await build({entryPoints:[resolve(here,'chat/index.tsx')],outfile:out,bundle:true,minify:true,format:'esm',target:'es2022',supported:{'template-literal':false},jsx:'automatic',nodePaths:[modules,resolve(here,"node_modules")],alias:{shiki:resolve(here,'chat/highlight.ts'),'tailwind-variants':resolve(modules,'tailwind-variants/dist/config-entry.js')},define:{'process.env.NODE_ENV':'"production"'},metafile:true,legalComments:'eof'});
const components=['chat-message','markdown','code-block','chain-of-thought','chat-tool','chat-attachment','prompt-input','chat-loader','text-shimmer'];
const proCss=name=>readFile(resolve(modules,'@heroui-pro/react/dist/css/components',name+'.css'),'utf8');
const css=await Promise.all(components.map(proCss));
// Shell components open sheets, menus and tooltips in portals under <body>, outside any island, so their upstream CSS is not scoped.
const shellComponents=['sidebar','command'];
const shellCss=await Promise.all(shellComponents.map(proCss));
await writeFile(resolve(here,'../internal/web/static/chat-ui.css'),'/* HeroUI Pro 1.0.0-beta.11: upstream component CSS scoped to React islands. */\n/* Preload React Aria pressable behavior without injecting an inline style under CSP. */\n@layer { [data-react-aria-pressable] { touch-action: pan-x pan-y pinch-zoom; } }\n@scope (.pro-chat) {\n'+css.join('\n')+'\n}\n/* HeroUI Pro shell components (global). */\n'+shellCss.join('\n')+'\n'+(await Promise.all(['sidebar.css','command.css','file-workbench.css','file-preview.css','file-actions.css'].map(name=>readFile(resolve(here,'chat',name),'utf8')))).join('\n'));
await writeFile(resolve(here,'build-meta.json'),JSON.stringify({packages:{'@heroui-pro/react':'1.0.0-beta.11',react:'19.3.1',shiki:'4.6.0'},output_bytes:result.metafile.outputs[out]?.bytes || (await readFile(out)).length,components,shell_components:shellComponents},null,2)+'\n');
console.log(`Built ${out}: ${(await readFile(out)).length} bytes`);
