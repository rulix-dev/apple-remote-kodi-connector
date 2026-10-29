# Possible improvements

## Pausing when the TV is turned off (network detection)

It works, but it can be improved:

- **About 8 s of the movie are lost.** The LG TV takes a few seconds to leave
  the network after turning off, plus the 2 s polling and the 3 s without answer.
  Idea: when pausing automatically, seek back that amount with `Player.Seek`
  (e.g. relative `{"seconds": -10}`).
- **A network outage on the TV side would pause the movie.** Ideas:
  - Require the TV to be unreachable for a long continuous time (e.g. 1 min)
    so that short outages do not matter; combine it with the seek back above.
  - The idle warning ("Are you still there?") already covers the general case.
- Switching the TV to another HDMI input is not detected (the TV stays on the
  network). It would need the webOS API with pairing, which would also allow
  turning the TV off from the Apple Remote.
