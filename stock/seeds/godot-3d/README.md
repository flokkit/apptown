# Duck Storm

A first-person game for the browser: rubber ducks waddle toward you, you
pop them. Made with Godot 4 and shipped as a web export.

## Run it

    docker build -t duck-storm . && docker run --rm -p 8080:8080 duck-storm

Open http://localhost:8080. Click to play, WASD to move, mouse to look,
click or Space to shoot, Esc frees the mouse. On a phone: drag to look,
tap to shoot, hold a second finger down to walk.

## Change it

Every rule is a named number at the top of a script: `main.gd` (how often
ducks come, how many), `duck.gd` (how fast they walk, how they bob),
`player.gd` (walking speed, look speed). Scenes are `main.tscn` and
`duck.tscn`.

## Configuration

None. The game keeps no data.
