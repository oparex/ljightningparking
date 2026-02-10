# Lightning Parking Payment App ⚡

A simple web application for paying parking fees with Bitcoin Lightning Network.

## Features

- 🚗 Simple one-page interface for parking payment
- ⚡ Bitcoin Lightning Network payments via LNbits
- 🌍 Multiple parking zones with different rates
- 💶 Automatic EUR to BTC conversion
- 📱 QR code generation for mobile wallets
- 🔄 Real-time payment verification
- 📡 Automatic notification to parking server upon payment

## Parking Zones

The app supports multiple parking zones with different hourly rates and maximum parking times. See `parking/zone.go` for the complete list.

## Requirements

- Go 1.21 or higher
- LNbits instance with API access
- Parking server API endpoint for payment notifications

## Installation

1. Clone the repository
2. Copy `.env.example` to `.env` and configure your settings
3. Run `go mod download` to install dependencies
4. Build the application: `go build -o parking-app`

## Configuration

Set the following environment variables:

- `LNBITS_URL`: Your LNbits server URL (default: https://legend.lnbits.com)
- `LNBITS_API_KEY`: Your LNbits Invoice/Read API key (required)
- `CALLBACK_URL`: The parking server endpoint to notify when payment is confirmed (required)
- `CALLBACK_API_KEY`: Optional API key for the parking server callback
- `PORT`: Server port (default: 8080)

## Usage

Run the application:

```bash
export LNBITS_API_KEY="your_api_key"
export CALLBACK_URL="https://your-parking-server.com/api/parking"
./parking-app
```

Or use environment variables from a file:

```bash
source .env
./parking-app
```

The application will start on `http://localhost:8080`

## How It Works

1. User enters license plate, selects parking zone, and chooses duration
2. Backend validates the input and calculates the parking fee in EUR
3. EUR amount is converted to BTC (satoshis) using real-time exchange rates
4. LNbits generates a Lightning invoice
5. User sees the invoice as a QR code and text
6. Application polls LNbits every 2 seconds to check payment status
7. When paid, the parking data is sent to the configured callback server
8. Success message is displayed and modal closes automatically

## API Endpoints

- `GET /` - Main page with the parking form
- `POST /submit` - Process parking request and generate invoice
- `GET /check-payment/:payment_hash` - Check if invoice has been paid

## Development

The application uses:

- [Gin](https://github.com/gin-gonic/gin) for the web framework
- [LNbits API](https://lnbits.com) for Lightning invoice generation
- [Bitstamp API](https://www.bitstamp.net/api/) for BTC/EUR price data

## License

MIT
