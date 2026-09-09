# Playwright's own image, pinned to the exact same version as
# package.json's @playwright/test — the browsers baked into this image and
# the test runner's own version have to match, or Playwright refuses to
# launch at all. package.json alone pins that with a caret range
# (^1.62.1), which npm install would happily satisfy with a newer
# @playwright/test the moment one is published — found live, deploying
# 1.8.2: @playwright/test resolved to 1.63.0 mid-build while this image
# stayed on 1.62.1's browsers, and every post-promotion real-login check
# failed with "Executable doesn't exist," aborting an otherwise-healthy
# rollout. package-lock.json already pins the exact 1.62.1 that matches
# this base image — npm ci is what actually makes that lockfile
# load-bearing instead of decorative, since npm install ignores it when
# nothing forces it to be present.
FROM mcr.microsoft.com/playwright:v1.62.1-noble

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY playwright.config.ts ./
COPY tests ./tests
RUN chown -R pwuser:pwuser /app

# The base image's own non-root user — already the account its browsers and
# their sandboxing are set up for, so this is not a chmod-and-hope situation.
USER pwuser

ENTRYPOINT ["npx", "playwright", "test"]
