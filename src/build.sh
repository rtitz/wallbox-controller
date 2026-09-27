#!/bin/bash

platforms=( "darwin/arm64" "darwin/amd64" "linux/arm" "linux/arm64" "linux/amd64" "linux/386" "windows/386" "windows/amd64" )

cd $( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )
package_name=$(cd .. && basename $(pwd) && cd - >/dev/null 2>&1)
output_directory="../bin/"

# 1. Generate a timestamp build number (Format: YYYYMMDD-HHMMSS)
build_number=$(date -u +'%Y-%m-%dT%H:%M:%SZ')

# 2. Path to the variable in your Go application
go_var_path="main.BuildNumber"

mkdir -p $output_directory >/dev/null 2>&1

echo "Downloading required modules..."
go get -u && go mod tidy

for platform in "${platforms[@]}"
do
    platform_split=(${platform//\// })
    GOOS=${platform_split[0]}
    GOARCH=${platform_split[1]}
    
    # Filenames restored strictly to your original layout (no build numbers in names)
    if [ $GOOS = "darwin" ]; then
        output_name=$package_name'_macos-'$GOARCH
    else
        output_name=$package_name'_'$GOOS'-'$GOARCH
    fi

    if [ $GOOS = "windows" ]; then
        output_name+='.exe'
    fi

    echo "Building $GOOS/$GOARCH output: $output_name (Embedding Build: $build_number)"

    # 3. Injecting the timestamp via the -X linker flag
    env GOOS=$GOOS GOARCH=$GOARCH go build -ldflags "-s -w -X ${go_var_path}=${build_number}" -o $output_name $package
    if [ $? -ne 0 ]; then
           echo 'An error has occurred! Aborting the script execution...'
        exit 1
    fi
    mv $output_name $output_directory
done
