extends Node3D
## Duck Storm: rubber ducks drift in from the edges of the field and waddle
## toward you. Pop them before they reach you. Every rule is a named number
## below, so changing the game is changing a number.

# How often a new duck appears, in seconds.
const SPAWN_EVERY: float = 1.6
# How far from the middle ducks appear.
const SPAWN_RADIUS: float = 30.0
# Ducks never outnumber this; the storm stays fair.
const MAX_DUCKS: int = 14

var ducks_popped: int = 0
var spawn_timer: float = 0.0

@onready var ducks: Node3D = $Ducks
@onready var player: CharacterBody3D = $Player
@onready var score_label: Label = $HUD/Score

var duck_scene: PackedScene = preload("res://duck.tscn")


func _process(delta: float) -> void:
	spawn_timer += delta
	if spawn_timer >= SPAWN_EVERY and ducks.get_child_count() < MAX_DUCKS:
		spawn_timer = 0.0
		spawn_duck()


# A duck appears somewhere on a ring around the field and heads for the player.
func spawn_duck() -> void:
	var duck: Node3D = duck_scene.instantiate()
	var angle: float = randf() * TAU
	duck.position = Vector3(cos(angle) * SPAWN_RADIUS, 0.6, sin(angle) * SPAWN_RADIUS)
	duck.set("target", player)
	duck.popped.connect(on_duck_popped)
	ducks.add_child(duck)


# Popping a duck scores one point; the label says so at once.
func on_duck_popped() -> void:
	ducks_popped += 1
	score_label.text = "Ducks popped: %d" % ducks_popped
