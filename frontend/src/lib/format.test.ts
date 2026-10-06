import test from 'node:test'
import assert from 'node:assert/strict'
import { formatBytes } from './format.ts'

test('formatBytes', () => {
  assert.equal(formatBytes(0), '0 B')
  assert.equal(formatBytes(1023), '1023 B')
  assert.equal(formatBytes(1024), '1.0 KB')
  assert.equal(formatBytes(1536), '1.5 KB')
  assert.equal(formatBytes(5 * 1024 * 1024), '5.0 MB')
  assert.equal(formatBytes(250 * 1024 * 1024), '250 MB')
  assert.equal(formatBytes(3.2 * 1024 ** 3), '3.2 GB')
  assert.equal(formatBytes(2 * 1024 ** 4), '2.0 TB')
  assert.equal(formatBytes(5000 * 1024 ** 4), '5000 TB')
  assert.equal(formatBytes(-1), '-')
  assert.equal(formatBytes(NaN), '-')
})
