import { resourceNameError } from './resourceName.js'

function assertEqual(actual: string | null, expected: string | null, label: string) {
  if (actual !== expected) throw new Error(`${label}: expected ${String(expected)}, got ${String(actual)}`)
}

assertEqual(resourceNameError('orders-prod'), null, 'valid name')
assertEqual(resourceNameError('Orders-prod'), 'name must use lowercase letters, numbers, and hyphens, and start and end with a letter or number.', 'uppercase is rejected')
assertEqual(resourceNameError('orders_prod'), 'name must use lowercase letters, numbers, and hyphens, and start and end with a letter or number.', 'underscore is rejected')
assertEqual(resourceNameError(' orders-prod'), 'name cannot start or end with whitespace.', 'leading whitespace is rejected')
assertEqual(resourceNameError('', 'resource name'), 'resource name is required.', 'empty name is rejected')
