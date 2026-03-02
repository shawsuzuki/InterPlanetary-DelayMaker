# Sample Files for Demo

These files can be used to demonstrate file transfer over the delayed link.

## Usage

Copy a file to Mars and transfer it back via the delayed network:

```bash
# Copy sample image into Earth container
docker cp samples/mars_sol0.jpeg earth:/tmp/

# Transfer from Earth to Mars using netcat (start receiver on Mars first)
docker exec mars sh -c "nc -l -p 9000 > /tmp/received.png" &
docker exec earth sh -c "nc 10.0.0.3 9000 < /tmp/mars_sol0.jpeg"

# Note: With 3-min Mars delay, ARP alone takes ~6 min (request + reply)
# Use Demo preset (5s) for quick demonstrations
```

## Files

- `mars_sol0.jpeg` - Small test image (64x64 px)

## Tips for BoF Demo

1. Start with **Demo (5s)** preset to show the system works
2. Run `docker exec earth ping -W 30 10.0.0.3` to show delayed ping
3. Switch to **Moon (1.3s)** to show a realistic but quick scenario
4. Switch to **Mars (closest)** to show the real challenge of interplanetary comms
