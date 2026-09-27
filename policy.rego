package reflex.authz

default allow = false

# Example policy: ONLY allow rollout restarts of safe deployments
allow {
    input.action == "rollout"
    input.resource == "restart"
    # To truly lock this down, we would list specific allowed targets here
    # e.g., input.target == "deploy/redis"
}
