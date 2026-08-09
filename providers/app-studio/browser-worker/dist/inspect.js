import { createHash } from 'node:crypto';
import { chromium } from 'playwright';
const VIEWPORT = { width: 1280, height: 720 };
const MAX_ASSERTIONS = 12;
const MAX_CONSOLE_EVENTS = 50;
const MAX_NETWORK_EVENTS = 50;
const MAX_MESSAGE_CHARS = 1_000;
const MAX_SNAPSHOT_CHARS = 30_000;
const MAX_SCREENSHOT_BYTES = 2 * 1024 * 1024;
const DEFAULT_TIMEOUT_MS = 15_000;
const MAX_TIMEOUT_MS = 30_000;
const READ_ONLY_METHODS = new Set(['GET', 'HEAD', 'OPTIONS']);
const FAILURE_RESOURCE_TYPES = new Set(['document', 'script']);
export class RequestValidationError extends Error {
}
export class PlaywrightInspector {
    browserPromise;
    async health() {
        await this.browser();
    }
    async inspect(input) {
        const request = validateInspectRequest(input);
        const target = new URL(request.url);
        const timeout = boundedTimeout(process.env.BROWSER_WORKER_TIMEOUT_MS);
        const browser = await this.browser();
        const context = await browser.newContext({
            acceptDownloads: false,
            bypassCSP: false,
            ignoreHTTPSErrors: process.env.BROWSER_WORKER_IGNORE_HTTPS_ERRORS === 'true',
            javaScriptEnabled: true,
            serviceWorkers: 'block',
            viewport: VIEWPORT,
        });
        try {
            const page = await context.newPage();
            page.setDefaultTimeout(Math.min(timeout, 5_000));
            const consoleEvidence = [];
            const networkEvidence = [];
            const applicationFailures = [];
            page.on('console', (message) => {
                pushBounded(consoleEvidence, {
                    level: bound(message.type(), 32),
                    message: sanitizeText(message.text(), MAX_MESSAGE_CHARS),
                }, MAX_CONSOLE_EVENTS);
            });
            page.on('pageerror', (error) => {
                const message = sanitizeText(error.message, MAX_MESSAGE_CHARS);
                applicationFailures.push(message || 'Uncaught page error');
                pushBounded(consoleEvidence, { level: 'pageerror', message }, MAX_CONSOLE_EVENTS);
            });
            page.on('requestfailed', (failedRequest) => {
                const failure = sanitizeText(failedRequest.failure()?.errorText ?? 'request failed', MAX_MESSAGE_CHARS);
                recordNetworkFailure(networkEvidence, failedRequest, failure);
                if (FAILURE_RESOURCE_TYPES.has(failedRequest.resourceType()))
                    applicationFailures.push(failure);
            });
            page.on('response', (response) => {
                const resourceType = response.request().resourceType();
                if (response.status() < 400)
                    return;
                const failure = `HTTP ${response.status()} ${bound(response.statusText(), 120)}`.trim();
                recordNetworkFailure(networkEvidence, response.request(), failure);
                if (FAILURE_RESOURCE_TYPES.has(resourceType))
                    applicationFailures.push(failure);
            });
            await page.route('**/*', async (route) => enforceReadOnlySameOrigin(route, target.origin, networkEvidence, applicationFailures));
            await page.routeWebSocket(/.*/, async (socket) => {
                const failure = 'blocked WebSocket connection';
                pushBounded(networkEvidence, { url: safeDisplayURL(socket.url()), method: 'WEBSOCKET', failure }, MAX_NETWORK_EVENTS);
                // WebSockets are intentionally unavailable during read-only inspection.
                // Vite opens one for HMR on every healthy development page, so the
                // policy block is evidence, not proof that the application failed.
                await socket.close({ code: 1008, reason: 'read-only preview inspection' });
            });
            let navigationFailure = '';
            try {
                await page.goto(request.url, { waitUntil: 'domcontentloaded', timeout });
                await page.waitForLoadState('networkidle', { timeout: Math.min(timeout, 3_000) }).catch(() => undefined);
            }
            catch (error) {
                navigationFailure = sanitizeText(errorMessage(error), MAX_MESSAGE_CHARS);
            }
            const finalURL = safeDisplayURL(page.url() || request.url);
            const title = navigationFailure ? '' : sanitizeText(await page.title().catch(() => ''), 500);
            const snapshot = navigationFailure ? '' : await semanticSnapshot(page, timeout);
            const assertions = navigationFailure ? [] : await evaluateAssertions(page, request.assertions ?? []);
            const screenshot = navigationFailure || !request.includeScreenshot
                ? undefined
                : await captureScreenshot(page);
            const assertionFailed = assertions.some((assertion) => !assertion.passed);
            const response = {
                status: navigationFailure || applicationFailures.length > 0 || assertionFailed ? 'failed' : 'succeeded',
                finalURL,
                title,
                snapshot,
                assertions,
                console: consoleEvidence,
                network: networkEvidence,
            };
            if (navigationFailure)
                response.failureKind = 'navigation';
            else if (applicationFailures.length > 0)
                response.failureKind = 'application';
            else if (assertionFailed)
                response.failureKind = 'assertion';
            if (screenshot)
                response.screenshot = screenshot;
            return response;
        }
        finally {
            await context.close();
        }
    }
    async close() {
        if (!this.browserPromise)
            return;
        const browser = await this.browserPromise;
        this.browserPromise = undefined;
        await browser.close();
    }
    async browser() {
        const chromiumSandbox = process.env.BROWSER_WORKER_CHROMIUM_SANDBOX !== 'false';
        this.browserPromise ??= chromium.launch({ chromiumSandbox, headless: true }).catch((error) => {
            this.browserPromise = undefined;
            throw error;
        });
        let browser = await this.browserPromise;
        if (!browser.isConnected()) {
            this.browserPromise = undefined;
            browser = await this.browser();
        }
        return browser;
    }
}
export function validateInspectRequest(input) {
    if (!input || typeof input !== 'object' || Array.isArray(input)) {
        throw new RequestValidationError('request body must be an object');
    }
    const raw = input;
    rejectUnknownKeys(raw, ['url', 'assertions', 'includeScreenshot'], 'request');
    if (typeof raw.url !== 'string' || raw.url.length === 0 || raw.url.length > 2_048) {
        throw new RequestValidationError('url must be a non-empty string of at most 2048 characters');
    }
    let target;
    try {
        target = new URL(raw.url);
    }
    catch {
        throw new RequestValidationError('url must be absolute');
    }
    if (!['http:', 'https:'].includes(target.protocol) || target.username || target.password) {
        throw new RequestValidationError('url must use http or https without embedded credentials');
    }
    if (raw.includeScreenshot !== undefined && typeof raw.includeScreenshot !== 'boolean') {
        throw new RequestValidationError('includeScreenshot must be a boolean');
    }
    if (raw.assertions !== undefined && !Array.isArray(raw.assertions)) {
        throw new RequestValidationError('assertions must be an array');
    }
    const assertions = (raw.assertions ?? []);
    if (assertions.length > MAX_ASSERTIONS) {
        throw new RequestValidationError(`assertions must contain at most ${MAX_ASSERTIONS} items`);
    }
    const normalized = assertions.map(validateAssertion);
    return {
        url: target.toString(),
        ...(normalized.length > 0 ? { assertions: normalized } : {}),
        ...(raw.includeScreenshot === true ? { includeScreenshot: true } : {}),
    };
}
function validateAssertion(value, index) {
    if (!value || typeof value !== 'object' || Array.isArray(value)) {
        throw new RequestValidationError(`assertions[${index}] must be an object`);
    }
    const raw = value;
    if (raw.kind === 'text_present') {
        rejectUnknownKeys(raw, ['kind', 'text', 'exact'], `assertions[${index}]`);
        const text = boundedRequiredString(raw.text, `assertions[${index}].text`);
        const exact = optionalBoolean(raw.exact, `assertions[${index}].exact`);
        return { kind: 'text_present', text, ...(exact === undefined ? {} : { exact }) };
    }
    if (raw.kind === 'role_present' || raw.kind === 'role_count') {
        rejectUnknownKeys(raw, ['kind', 'role', 'name', 'exact', 'min', 'max'], `assertions[${index}]`);
        const role = boundedRequiredString(raw.role, `assertions[${index}].role`);
        const name = optionalBoundedString(raw.name, `assertions[${index}].name`);
        const exact = optionalBoolean(raw.exact, `assertions[${index}].exact`);
        if (raw.kind === 'role_present') {
            if (raw.min !== undefined || raw.max !== undefined) {
                throw new RequestValidationError(`assertions[${index}] role_present does not accept min or max`);
            }
            return { kind: 'role_present', role, ...(name ? { name } : {}), ...(exact === undefined ? {} : { exact }) };
        }
        const min = optionalCount(raw.min, `assertions[${index}].min`);
        const max = optionalCount(raw.max, `assertions[${index}].max`);
        if (min !== undefined && max !== undefined && min > max) {
            throw new RequestValidationError(`assertions[${index}].min cannot exceed max`);
        }
        return {
            kind: 'role_count',
            role,
            ...(name ? { name } : {}),
            ...(exact === undefined ? {} : { exact }),
            ...(min === undefined ? {} : { min }),
            ...(max === undefined ? {} : { max }),
        };
    }
    throw new RequestValidationError(`assertions[${index}].kind is unsupported`);
}
async function enforceReadOnlySameOrigin(route, allowedOrigin, network, applicationFailures) {
    const request = route.request();
    let parsed;
    try {
        parsed = new URL(request.url());
    }
    catch {
        await route.abort('blockedbyclient');
        return;
    }
    const localProtocol = parsed.protocol === 'data:' || parsed.protocol === 'blob:' || parsed.protocol === 'about:';
    const sameOrigin = localProtocol || parsed.origin === allowedOrigin;
    const readOnly = READ_ONLY_METHODS.has(request.method().toUpperCase());
    if (sameOrigin && readOnly) {
        await route.continue();
        return;
    }
    const failure = sameOrigin ? 'blocked non-read-only request' : 'blocked cross-origin request';
    recordNetworkFailure(network, request, failure);
    if (FAILURE_RESOURCE_TYPES.has(request.resourceType()))
        applicationFailures.push(failure);
    await route.abort('blockedbyclient');
}
async function evaluateAssertions(page, assertions) {
    const results = [];
    for (const assertion of assertions) {
        let count = 0;
        if (assertion.kind === 'text_present') {
            count = await page.getByText(assertion.text, { exact: assertion.exact ?? false }).count();
        }
        else {
            count = await page.getByRole(assertion.role, {
                ...(assertion.name ? { name: assertion.name } : {}),
                exact: assertion.exact ?? false,
            }).count();
        }
        const min = assertion.kind === 'role_count' ? assertion.min ?? 0 : 1;
        const max = assertion.kind === 'role_count' ? assertion.max : undefined;
        const passed = count >= min && (max === undefined || count <= max);
        results.push({
            ...assertion,
            passed,
            actualCount: count,
            ...(!passed ? { message: max === undefined ? `expected at least ${min}, found ${count}` : `expected ${min}..${max}, found ${count}` } : {}),
        });
    }
    return results;
}
async function semanticSnapshot(page, timeout) {
    try {
        const raw = await page.locator('body').ariaSnapshot({ timeout: Math.min(timeout, 3_000) });
        return sanitizeText(raw, MAX_SNAPSHOT_CHARS);
    }
    catch {
        return '';
    }
}
async function captureScreenshot(page) {
    const bytes = await page.screenshot({ animations: 'disabled', fullPage: false, type: 'png' });
    if (bytes.byteLength > MAX_SCREENSHOT_BYTES)
        return undefined;
    return {
        mimeType: 'image/png',
        base64: bytes.toString('base64'),
        width: VIEWPORT.width,
        height: VIEWPORT.height,
        sha256: createHash('sha256').update(bytes).digest('hex'),
    };
}
function recordNetworkFailure(target, request, failure) {
    const item = {
        url: safeDisplayURL(request.url()),
        method: bound(request.method().toUpperCase(), 16),
        failure: sanitizeText(failure, MAX_MESSAGE_CHARS),
    };
    if (target.some((existing) => existing.url === item.url && existing.method === item.method && existing.failure === item.failure))
        return;
    pushBounded(target, item, MAX_NETWORK_EVENTS);
}
function safeDisplayURL(raw) {
    try {
        const parsed = new URL(raw);
        parsed.username = '';
        parsed.password = '';
        parsed.search = '';
        parsed.hash = '';
        return bound(parsed.toString(), 2_048);
    }
    catch {
        return '';
    }
}
function sanitizeText(raw, max) {
    return bound(raw
        .replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f]/gu, '')
        .replace(/([?&](?:token|key|secret|password|signature|sig|code)=)[^&\s]+/giu, '$1[REDACTED]')
        .replace(/\b(?:Bearer|Basic)\s+[A-Za-z0-9._~+\/-]+=*/giu, '[REDACTED_AUTH]'), max);
}
function bound(value, max) {
    if (value.length <= max)
        return value;
    return `${value.slice(0, Math.max(0, max - 3))}...`;
}
function pushBounded(target, value, max) {
    if (target.length < max)
        target.push(value);
}
function rejectUnknownKeys(raw, allowed, field) {
    const unexpected = Object.keys(raw).find((key) => !allowed.includes(key));
    if (unexpected)
        throw new RequestValidationError(`${field} contains unknown field ${unexpected}`);
}
function boundedRequiredString(value, field) {
    if (typeof value !== 'string' || value.trim() === '' || value.length > 500) {
        throw new RequestValidationError(`${field} must be a non-empty string of at most 500 characters`);
    }
    return value.trim();
}
function optionalBoundedString(value, field) {
    if (value === undefined)
        return undefined;
    return boundedRequiredString(value, field);
}
function optionalBoolean(value, field) {
    if (value === undefined)
        return undefined;
    if (typeof value !== 'boolean')
        throw new RequestValidationError(`${field} must be a boolean`);
    return value;
}
function optionalCount(value, field) {
    if (value === undefined)
        return undefined;
    if (!Number.isSafeInteger(value) || Number(value) < 0 || Number(value) > 10_000) {
        throw new RequestValidationError(`${field} must be an integer from 0 through 10000`);
    }
    return Number(value);
}
function boundedTimeout(raw) {
    const parsed = Number(raw);
    if (!Number.isFinite(parsed) || parsed <= 0)
        return DEFAULT_TIMEOUT_MS;
    return Math.min(Math.floor(parsed), MAX_TIMEOUT_MS);
}
function errorMessage(error) {
    return error instanceof Error ? error.message : String(error);
}
//# sourceMappingURL=inspect.js.map