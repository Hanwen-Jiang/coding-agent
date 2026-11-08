// Refresh the checked-in, same-origin CSS distribution. No runtime npm/CDN dependency.
import {readFile, copyFile} from 'node:fs/promises';
import {resolve} from 'node:path';
import {fileURLToPath} from 'node:url';
import {createHash} from 'node:crypto';
const source = process.argv[2];
if (!source) throw new Error('Usage: node scripts/ui/vendor-heroui.mjs /path/to/node_modules/@heroui/styles');
const pkg = JSON.parse(await readFile(resolve(source, 'package.json'), 'utf8'));
if (pkg.name !== '@heroui/styles' || pkg.version !== '3.2.7') throw new Error('Expected @heroui/styles 3.2.7');
const root = fileURLToPath(new URL('../../', import.meta.url));
await copyFile(resolve(source, 'dist/heroui.min.css'), resolve(root, 'internal/web/static/heroui.css'));
await copyFile(resolve(source, 'LICENSE'), resolve(root, 'internal/web/static/heroui-LICENSE.txt'));
const bytes = await readFile(resolve(root, 'internal/web/static/heroui.css'));
console.log(`@heroui/styles ${pkg.version}: ${bytes.length} bytes, sha256 ${createHash('sha256').update(bytes).digest('hex')}`);
