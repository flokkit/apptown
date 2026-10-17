extends CharacterBody3D
## The player: first-person, walks with WASD or arrows (or by holding a
## second finger on the screen), looks with the mouse (after a click, because
## browsers require one before capturing the pointer) or by dragging a
## finger, shoots with Space, a click or a tap.

# Walking speed in metres per second.
const SPEED: float = 7.0
# How much the view turns per pixel of mouse or finger movement.
const LOOK_SPEED: float = 0.0025
# How high the player can look up or down, in radians.
const MAX_PITCH: float = 1.3
# Gravity keeps feet on the floor.
const GRAVITY: float = 20.0

var pitch: float = 0.0
var touch_dragging: bool = false
var touch_moved: bool = false
# True while a second finger is held down: the player walks forward.
var touch_walking: bool = false

@onready var camera: Camera3D = $Camera
@onready var ray: RayCast3D = $Camera/Ray


func _ready() -> void:
	# Input actions live here, in words, not in project.godot's long form.
	add_key("move_forward", KEY_W, KEY_UP)
	add_key("move_back", KEY_S, KEY_DOWN)
	add_key("move_left", KEY_A, KEY_LEFT)
	add_key("move_right", KEY_D, KEY_RIGHT)
	add_key("shoot", KEY_SPACE, KEY_ENTER)


func add_key(action: String, key: Key, alt_key: Key) -> void:
	if InputMap.has_action(action):
		return
	InputMap.add_action(action)
	for k in [key, alt_key]:
		var event := InputEventKey.new()
		event.physical_keycode = k
		InputMap.action_add_event(action, event)


func _unhandled_input(event: InputEvent) -> void:
	# Mouse: the first click captures the pointer; later clicks shoot.
	if event is InputEventMouseButton and event.pressed and event.button_index == MOUSE_BUTTON_LEFT:
		if Input.mouse_mode != Input.MOUSE_MODE_CAPTURED:
			Input.mouse_mode = Input.MOUSE_MODE_CAPTURED
		else:
			shoot()
	if event is InputEventMouseMotion and Input.mouse_mode == Input.MOUSE_MODE_CAPTURED:
		look(event.relative)
	# Space (or Enter) shoots, for players on a keyboard.
	if event.is_action_pressed("shoot"):
		shoot()
	# Escape gives the mouse back.
	if event is InputEventKey and event.pressed and event.keycode == KEY_ESCAPE:
		Input.mouse_mode = Input.MOUSE_MODE_VISIBLE
	# Touch: drag to look, a tap without dragging shoots, and holding a second
	# finger down walks forward.
	if event is InputEventScreenTouch and event.index > 0:
		touch_walking = event.pressed
		return
	if event is InputEventScreenTouch:
		if event.pressed:
			touch_dragging = true
			touch_moved = false
		else:
			touch_dragging = false
			if not touch_moved:
				shoot()
	if event is InputEventScreenDrag and event.index == 0 and touch_dragging:
		touch_moved = true
		look(event.relative)


# Turning the view: left/right turns the body, up/down tilts the camera.
func look(relative: Vector2) -> void:
	rotate_y(-relative.x * LOOK_SPEED)
	pitch = clamp(pitch - relative.y * LOOK_SPEED, -MAX_PITCH, MAX_PITCH)
	camera.rotation.x = pitch


func _physics_process(delta: float) -> void:
	if not is_on_floor():
		velocity.y -= GRAVITY * delta
	var input_dir: Vector2 = Input.get_vector("move_left", "move_right", "move_forward", "move_back")
	if touch_walking and input_dir == Vector2.ZERO:
		input_dir = Vector2(0, -1)
	var direction: Vector3 = (transform.basis * Vector3(input_dir.x, 0, input_dir.y)).normalized()
	velocity.x = direction.x * SPEED
	velocity.z = direction.z * SPEED
	move_and_slide()


# A shot is a straight line from the camera; whatever it hits that can pop, pops.
func shoot() -> void:
	if ray.is_colliding():
		var hit: Object = ray.get_collider()
		if hit != null and hit.has_method("pop"):
			hit.pop()
