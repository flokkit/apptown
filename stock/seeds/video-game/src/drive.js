// How the car's speed changes in one frame: forward speeds it up, back
// slows it down and then reverses, drag pulls it towards a stop, and the
// speed stays between full reverse and top speed. Kept apart from the scene
// so a test can check it without a browser.
export const TOP_SPEED = 22
export const REVERSE_SPEED = -8

export function nextSpeed(speed, forward, back, dt) {
  if (forward) speed += 24 * dt
  if (back) speed -= 18 * dt
  speed -= speed * 1.6 * dt // drag
  return Math.max(REVERSE_SPEED, Math.min(TOP_SPEED, speed))
}
