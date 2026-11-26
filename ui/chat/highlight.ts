// Lazy Shiki entrypoint: keeps heavy language grammars out of the initial chat-ui.js bundle.
let corePromise: Promise<typeof import('./highlight-core')> | null = null;

export async function codeToTokens(code: string, options: Record<string, unknown>) {
 corePromise ||= import('./highlight-core');
 const mod = await corePromise;
 return mod.codeToTokens(code, options);
}
