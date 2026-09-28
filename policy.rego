package reflex.authz

default allow = false

# Allow rollout restart
allow {
    input.action == "rollout"
    input.resource == "restart"
}

# Allow rollout undo
allow {
    input.action == "rollout"
    input.resource == "undo"
}

# Allow scale
allow {
    input.action == "scale"
    startswith(input.resource, "deploy/")
}
