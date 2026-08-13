import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  appType: 'custom',
  cacheDir: '/tmp/faros-vite-production-form',
  configFile: false,
  server: { middlewareMode: true },
})
const {
  fieldID,
  productionFieldID,
  arrayInputValue,
  arrayInputValues,
  productionFormValuesFromSchema,
  renameMapKey,
  validateProductionValues,
  visibleProductionProperties,
} = await vite.ssrLoadModule('/src/productionForm.ts')
test.after(async () => vite.close())

const schema = {
  type: 'object',
  properties: {
    name: { type: 'string' },
    webImage: { type: 'string' },
    farosRedeployRevision: { type: 'string', description: 'Computed by the platform' },
    access: { type: 'string', enum: ['public', 'private'], default: 'public' },
    database: {
      type: 'object',
      default: {},
      properties: {
        size: { type: 'string', enum: ['small', 'medium', 'large'], default: 'small' },
        version: { type: 'string', enum: ['15', '16'], default: '16' },
      },
    },
    webEnv: { type: 'object', additionalProperties: { type: 'string' }, default: {} },
    emailDomains: { type: 'array', items: { type: 'string' }, default: [] },
  },
}

test('filters platform and image-owned fields while retaining persisted production values', () => {
  assert.deepEqual(
    visibleProductionProperties(schema, ['webImage']).map(([name]) => name),
    ['access', 'database', 'webEnv', 'emailDomains'],
  )
  assert.deepEqual(
    productionFormValuesFromSchema(schema, {
      access: 'private',
      database: { size: 'large' },
      name: 'ignored',
      webImage: 'ignored',
      farosRedeployRevision: 'ignored',
    }, ['webImage']),
    { access: 'private', database: { size: 'large', version: '16' }, webEnv: {}, emailDomains: [] },
  )
})

test('hydrates defaults for first render without overwriting explicit values', () => {
  assert.deepEqual(productionFormValuesFromSchema(schema, {}, ['webImage']), {
    access: 'public',
    database: { size: 'small', version: '16' },
    webEnv: {},
    emailDomains: [],
  })
  assert.equal(fieldID('database.size'), 'production-input-database-size')
})

test('recursive field ids include the full sibling object path', () => {
  const databaseSize = productionFieldID('database', 'size')
  const cacheSize = productionFieldID('cache', ['size'])

  assert.equal(databaseSize, 'production-input-database-size')
  assert.equal(cacheSize, 'production-input-cache-size')
  assert.notEqual(databaseSize, cacheSize)
  assert.equal(`${databaseSize}-description`, 'production-input-database-size-description')
  assert.equal(`${cacheSize}-description`, 'production-input-cache-size-description')
})

test('converts array inputs to the schema item type', async () => {
  assert.equal(arrayInputValue(['example.com', 'example.org']), 'example.com\nexample.org')
  assert.deepEqual(arrayInputValues('15\n16', { type: 'integer' }), [15, 16])
  assert.deepEqual(arrayInputValues('1.5\nnope', { type: 'integer' }), [1.5, Number.NaN])
  assert.deepEqual(arrayInputValues('true\nfalse', { type: 'boolean' }), [true, false])
  assert.deepEqual(arrayInputValues('yes', { type: 'boolean' }), ['yes'])
  const objectItems = {
    type: 'object', properties: {
      path: { type: 'string' },
      fqdn: { type: 'string', description: 'Computed by the platform' },
    },
  }
  assert.deepEqual(arrayInputValues('{"path":"/","fqdn":"spoofed"}', objectItems), [{ path: '/' }])
  assert.deepEqual(arrayInputValues('not-json', objectItems), ['not-json'])
})

test('renames environment keys without losing values or overwriting an existing key', () => {
  assert.deepEqual(renameMapKey({ OLD: 'value', KEEP: 'other' }, 'OLD', 'NEW'), { NEW: 'value', KEEP: 'other' })
  assert.deepEqual(renameMapKey({ OLD: 'value', KEEP: 'other' }, 'OLD', 'KEEP'), { OLD: 'value', KEEP: 'other' })
})

test('reports nested schema errors that block an invalid production payload', () => {
  const validationSchema = {
    type: 'object', required: ['replicas', 'database'], properties: {
      replicas: { type: 'integer', minimum: 1, maximum: 5 },
      hostname: { type: 'string', minLength: 3, pattern: '^[a-z]+$' },
      database: { type: 'object', required: ['size'], properties: { size: { type: 'string', enum: ['small', 'medium', 'large'] } } },
      routes: { type: 'array', items: { type: 'object' } },
    },
  }
  const errors = validateProductionValues(validationSchema, {
    replicas: 0, hostname: 'A', database: { size: 'huge' }, routes: ['not-json'],
  })
  assert.deepEqual(errors.map((error) => error.path), ['replicas', 'hostname', 'hostname', 'database.size', 'routes.0'])
  assert.deepEqual(validateProductionValues(validationSchema, {
    replicas: 2, hostname: 'shop', database: { size: 'medium' }, routes: [{ path: '/' }],
  }), [])
})
