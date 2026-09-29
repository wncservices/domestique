# Weather

Weather-aware suggestions use the [Open-Meteo](https://open-meteo.com) forecast API.
Design: `docs/superpowers/specs/2026-09-29-indoor-and-weather-design.md`.

## Terms

The free public API (`https://api.open-meteo.com`) is for **non-commercial use
only**, capped at 10,000 calls a day, 5,000 an hour and 600 a minute, and requires
CC-BY 4.0 attribution (checked 2026-09-29, <https://open-meteo.com/en/terms>). The UI
shows "Weather data by Open-Meteo.com" with a link for that reason.

A **commercial deployment (ad-supported or subscription) needs a paid Open-Meteo
plan.** Point `weather.base_url` at the commercial endpoint and set
`OPEN_METEO_API_KEY` (sent as `apikey`, never stored or logged). A self-hosted
Open-Meteo instance works the same way through `weather.base_url`.

## Configuration

```yaml
weather:
  enabled: true                          # default; false hides the feature (412)
  base_url: https://api.open-meteo.com   # default
```

## Privacy

Weather is opt-in per rider. Only the rider's chosen town, latitude and longitude
rounded to two decimals (about 1.1 km), is ever sent, and never a route, GPX or ride
coordinate. Coordinates and place names are not logged and not returned by the API.
Results are cached for an hour per rounded location.
