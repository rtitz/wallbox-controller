# Data dir

This directory should contain the output json files from wallbox-monitor and solar-monitor.

```
cd ${HOME}/git/charging-calculator/src/data && rsync -avz --include='*/' --include='*.json' --exclude='*' 192.168.1.4:/root/{solar,wallbox}-monitor/ . && cd .. && go run .
```
