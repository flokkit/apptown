// Star Drive — the starter game for this way of working. A tiny complete
// 3D game: drive a car around a field and collect stars. It is NOT your
// game yet; tell the helper what yours should be and it will reshape or
// replace this.
import * as THREE from 'three'
import { nextSpeed } from './drive.js'

const scene = new THREE.Scene()
scene.background = new THREE.Color(0x0b0e1a)
scene.fog = new THREE.Fog(0x0b0e1a, 40, 120)

const camera = new THREE.PerspectiveCamera(60, innerWidth / innerHeight, 0.1, 300)
const renderer = new THREE.WebGLRenderer({ antialias: true })
renderer.setSize(innerWidth, innerHeight)
renderer.shadowMap.enabled = true
document.body.appendChild(renderer.domElement)

addEventListener('resize', () => {
  camera.aspect = innerWidth / innerHeight
  camera.updateProjectionMatrix()
  renderer.setSize(innerWidth, innerHeight)
})

// Lights
const sun = new THREE.DirectionalLight(0xffffff, 2.2)
sun.position.set(20, 40, 10)
sun.castShadow = true
sun.shadow.camera.left = -60
sun.shadow.camera.right = 60
sun.shadow.camera.top = 60
sun.shadow.camera.bottom = -60
scene.add(sun, new THREE.AmbientLight(0x8899ff, 0.5))

// Ground
const ground = new THREE.Mesh(
  new THREE.PlaneGeometry(200, 200),
  new THREE.MeshStandardMaterial({ color: 0x1d5c2f }),
)
ground.rotation.x = -Math.PI / 2
ground.receiveShadow = true
scene.add(ground)

// A few scattered trees so motion is visible
for (let i = 0; i < 40; i++) {
  const trunk = new THREE.Mesh(
    new THREE.CylinderGeometry(0.3, 0.4, 2),
    new THREE.MeshStandardMaterial({ color: 0x6b4a2b }),
  )
  const top = new THREE.Mesh(
    new THREE.ConeGeometry(1.6, 3.2, 8),
    new THREE.MeshStandardMaterial({ color: 0x2e7d43 }),
  )
  top.position.y = 2.6
  const tree = new THREE.Group()
  tree.add(trunk, top)
  tree.position.set((Math.random() - 0.5) * 180, 1, (Math.random() - 0.5) * 180)
  if (tree.position.length() < 12) tree.position.setLength(20)
  tree.traverse((o) => { o.castShadow = true })
  scene.add(tree)
}

// The car
const car = new THREE.Group()
const body = new THREE.Mesh(
  new THREE.BoxGeometry(1.6, 0.6, 2.8),
  new THREE.MeshStandardMaterial({ color: 0xd23c3c }),
)
body.position.y = 0.5
const cabin = new THREE.Mesh(
  new THREE.BoxGeometry(1.2, 0.5, 1.2),
  new THREE.MeshStandardMaterial({ color: 0xeeeeee }),
)
cabin.position.set(0, 1.0, -0.2)
car.add(body, cabin)
for (const [x, z] of [[-0.8, 1.0], [0.8, 1.0], [-0.8, -1.0], [0.8, -1.0]]) {
  const wheel = new THREE.Mesh(
    new THREE.CylinderGeometry(0.35, 0.35, 0.3, 16),
    new THREE.MeshStandardMaterial({ color: 0x222222 }),
  )
  wheel.rotation.z = Math.PI / 2
  wheel.position.set(x, 0.35, z)
  car.add(wheel)
}
car.traverse((o) => { o.castShadow = true })
scene.add(car)

// Stars to collect
const hud = document.getElementById('hud')
let score = 0
const stars = []
const starGeometry = new THREE.OctahedronGeometry(0.7)
const starMaterial = new THREE.MeshStandardMaterial({
  color: 0xffd75e, emissive: 0xaa8820,
})
function spawnStar() {
  const star = new THREE.Mesh(starGeometry, starMaterial)
  star.position.set((Math.random() - 0.5) * 160, 1.2, (Math.random() - 0.5) * 160)
  star.castShadow = true
  stars.push(star)
  scene.add(star)
}
for (let i = 0; i < 12; i++) spawnStar()

// Driving, with keys or by touch. Keys: W/S or up/down drive and reverse,
// A/D or left/right steer. Touch or mouse: hold anywhere to drive, hold to
// the left or right of the middle to steer, hold near the bottom to reverse.
const keys = new Set()
addEventListener('keydown', (e) => keys.add(e.code))
addEventListener('keyup', (e) => keys.delete(e.code))
const pointer = { down: false, x: 0, y: 0 }
renderer.domElement.style.touchAction = 'none' // a drag steers, it does not scroll
renderer.domElement.addEventListener('pointerdown', (e) => {
  pointer.down = true
  pointer.x = e.clientX
  pointer.y = e.clientY
})
renderer.domElement.addEventListener('pointermove', (e) => {
  pointer.x = e.clientX
  pointer.y = e.clientY
})
for (const name of ['pointerup', 'pointercancel', 'pointerleave']) {
  renderer.domElement.addEventListener(name, () => { pointer.down = false })
}
let speed = 0
let heading = 0

const clock = new THREE.Clock()
function tick() {
  const dt = Math.min(clock.getDelta(), 0.05)

  // Pointer zones: the bottom fifth reverses; the left and right thirds steer.
  const pointerBack = pointer.down && pointer.y > innerHeight * 0.8
  const pointerSteer = pointer.down ? (pointer.x - innerWidth / 2) / (innerWidth / 6) : 0
  const forward = keys.has('KeyW') || keys.has('ArrowUp') || (pointer.down && !pointerBack)
  const back = keys.has('KeyS') || keys.has('ArrowDown') || pointerBack
  const left = keys.has('KeyA') || keys.has('ArrowLeft') || pointerSteer < -1
  const right = keys.has('KeyD') || keys.has('ArrowRight') || pointerSteer > 1

  speed = nextSpeed(speed, forward, back, dt)
  const steer = (left ? 1 : 0) - (right ? 1 : 0)
  heading += steer * 1.9 * dt * Math.min(1, Math.abs(speed) / 6) * Math.sign(speed || 1)

  car.rotation.y = heading
  car.position.x -= Math.sin(heading) * speed * dt * -1
  car.position.z -= Math.cos(heading) * speed * dt * -1
  car.position.x = Math.max(-98, Math.min(98, car.position.x))
  car.position.z = Math.max(-98, Math.min(98, car.position.z))

  for (let i = stars.length - 1; i >= 0; i--) {
    const star = stars[i]
    star.rotation.y += 2 * dt
    if (star.position.distanceTo(car.position) < 2.2) {
      scene.remove(star)
      stars.splice(i, 1)
      score++
      hud.textContent = `Stars: ${score}`
      spawnStar()
    }
  }

  // Chase camera
  const behind = new THREE.Vector3(Math.sin(heading), 0, Math.cos(heading))
  camera.position.lerp(
    car.position.clone().addScaledVector(behind, -9).add(new THREE.Vector3(0, 5.5, 0)),
    1 - Math.exp(-6 * dt),
  )
  camera.lookAt(car.position.clone().add(new THREE.Vector3(0, 1, 0)))

  renderer.render(scene, camera)
  requestAnimationFrame(tick)
}
tick()
