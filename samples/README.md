# Sample Files for Demo

These files can be used to demonstrate file transfer over the delayed link.

## Usage

Copy a file to Mars and transfer it back via the delayed network:

```bash
# Copy sample image into Earth container
docker cp samples/mars_sol0.jpeg earth:/tmp/

# Transfer from Earth to Mars using netcat (start receiver on Mars first)
docker exec mars sh -c "nc -l -p 9000 > /tmp/received.jpeg" &
docker exec earth sh -c "nc 10.0.0.3 9000 < /tmp/mars_sol0.jpeg"

# Transfer from Earth to Moon (much faster!)
docker exec moon sh -c "nc -l -p 9000 > /tmp/received.jpeg" &
docker exec earth sh -c "nc 10.1.0.3 9000 < /tmp/mars_sol0.jpeg"

# Note: With 3-min Mars delay, ARP alone takes ~6 min (request + reply)
# Use Demo preset (5s) for quick demonstrations
```

## Files

- `mars_sol0.jpeg` - Small test image (64x64 px)

## Tips for BoF Demo

1. Start with **Demo** preset to show the system works
2. Run `docker exec earth ping -W 30 10.0.0.3` to show delayed Mars ping
3. Run `docker exec earth ping -W 5 10.1.0.3` to show Moon ping (~2.6s RTT)
4. Switch to **Moon Only (1.3s)** to compare Moon vs Mars latency
5. Switch to **Mars (closest)** to show the real challenge of interplanetary comms
6. Open dashboard at `http://localhost:8080` to visualize delays and queues
