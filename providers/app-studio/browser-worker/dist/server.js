import { createServer as createHTTPServer } from 'node:http';
import { fileURLToPath } from 'node:url';
import { PlaywrightInspector, RequestValidationError } from './inspect.js';
const DEFAULT_PORT = 8090;
const MAX_REQUEST_BYTES = 64 * 1024;
const MAX_CONCURRENT_INSPECTIONS = 2;
export function createServer(inspector = new PlaywrightInspector()) {
    let activeInspections = 0;
    return createHTTPServer(async (request, response) => {
        try {
            if (request.method === 'GET' && request.url === '/healthz') {
                await inspector.health();
                writeJSON(response, 200, { status: 'ok' });
                return;
            }
            if (request.method === 'POST' && request.url === '/v1/inspect') {
                if (activeInspections >= MAX_CONCURRENT_INSPECTIONS) {
                    writeJSON(response, 429, { error: 'browser inspection capacity is exhausted' });
                    return;
                }
                activeInspections++;
                try {
                    const body = await readJSON(request);
                    writeJSON(response, 200, await inspector.inspect(body));
                }
                finally {
                    activeInspections--;
                }
                return;
            }
            writeJSON(response, 404, { error: 'not found' });
        }
        catch (error) {
            if (error instanceof RequestValidationError || error instanceof SyntaxError) {
                writeJSON(response, 400, { error: error.message });
                return;
            }
            if (request.method === 'GET' && request.url === '/healthz') {
                writeJSON(response, 503, { status: 'unavailable' });
                return;
            }
            writeJSON(response, 500, { error: 'browser inspection failed' });
        }
    });
}
async function readJSON(request) {
    const chunks = [];
    let size = 0;
    for await (const chunk of request) {
        const bytes = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
        size += bytes.byteLength;
        if (size > MAX_REQUEST_BYTES)
            throw new RequestValidationError('request body exceeds 64 KiB');
        chunks.push(bytes);
    }
    if (size === 0)
        throw new RequestValidationError('request body is required');
    return JSON.parse(Buffer.concat(chunks).toString('utf8'));
}
function writeJSON(response, status, value) {
    const body = JSON.stringify(value);
    response.writeHead(status, {
        'cache-control': 'no-store',
        'content-length': Buffer.byteLength(body),
        'content-type': 'application/json; charset=utf-8',
        'x-content-type-options': 'nosniff',
    });
    response.end(body);
}
if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
    const inspector = new PlaywrightInspector();
    const port = parsePort(process.env.PORT);
    const server = createServer(inspector);
    server.listen(port, '127.0.0.1', () => process.stdout.write(`App Studio browser worker listening on 127.0.0.1:${port}\n`));
    const shutdown = () => {
        server.close(() => void inspector.close().finally(() => process.exit(0)));
    };
    process.once('SIGINT', shutdown);
    process.once('SIGTERM', shutdown);
}
function parsePort(raw) {
    const port = Number(raw ?? DEFAULT_PORT);
    return Number.isSafeInteger(port) && port > 0 && port <= 65_535 ? port : DEFAULT_PORT;
}
//# sourceMappingURL=server.js.map