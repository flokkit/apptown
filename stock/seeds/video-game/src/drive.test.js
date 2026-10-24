import { test } from 'node:test'
import assert from 'node:assert/strict'
import { nextSpeed, TOP_SPEED, REVERSE_SPEED } from './drive.js'

test('holding forward speeds the car up, never past top speed', () => {
  let speed = 0
  for (let i = 0; i < 1000; i++) speed = nextSpeed(speed, true, false, 0.05)
  assert.ok(speed > 0 && speed <= TOP_SPEED)
})

test('letting go slows the car towards a stop', () => {
  assert.ok(nextSpeed(10, false, false, 0.05) < 10)
})

test('holding back reverses, never past full reverse', () => {
  let speed = 0
  for (let i = 0; i < 1000; i++) speed = nextSpeed(speed, false, true, 0.05)
  assert.ok(speed < 0 && speed >= REVERSE_SPEED)
})
