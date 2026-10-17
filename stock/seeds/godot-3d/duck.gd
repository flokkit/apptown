extends StaticBody3D
## A rubber duck: bobs as it waddles toward the player, pops into confetti
## when shot, and simply waddles off the far side of the field if it gets
## past you (no lives, no punishment — it is a duck).

signal popped

# Walking speed toward the player, in metres per second.
const WALK_SPEED: float = 2.5
# How fast and how high the duck bobs.
const BOB_SPEED: float = 6.0
const BOB_HEIGHT: float = 0.25
# A duck that gets within this distance of the player just wanders past.
const GIVE_UP_DISTANCE: float = 1.5

var target: Node3D = null
var time_alive: float = 0.0
var passing: bool = false

@onready var pop_particles: CPUParticles3D = $Pop


func _physics_process(delta: float) -> void:
	time_alive += delta
	if target == null:
		return
	var to_target: Vector3 = target.global_position - global_position
	to_target.y = 0.0
	if not passing and to_target.length() < GIVE_UP_DISTANCE:
		passing = true
	# Ducks face where they walk, and keep walking; a passing duck keeps its heading.
	if not passing:
		look_at(global_position + to_target, Vector3.UP)
	global_position += -global_transform.basis.z * WALK_SPEED * delta
	global_position.y = 0.6 + sin(time_alive * BOB_SPEED) * BOB_HEIGHT
	if global_position.length() > 60.0:
		queue_free()


# Popping: the confetti bursts, the duck is gone, the score hears about it.
func pop() -> void:
	popped.emit()
	pop_particles.reparent(get_parent())
	pop_particles.emitting = true
	get_tree().create_timer(1.0).timeout.connect(pop_particles.queue_free)
	queue_free()
