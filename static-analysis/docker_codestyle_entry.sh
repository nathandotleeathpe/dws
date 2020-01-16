#!/bin/bash

go get -u golang.org/x/lint/golint

GOLINTBIN=$(dirname $(go list -f {{.Target}} golang.org/x/lint/golint))
export PATH=$PATH:$GOLINTBIN

# Check non-vendor packages
mypkgs=$(go list ./pkg/... ./cmd/...)
echo "Checking non-vendor packages:"
echo "$mypkgs"

# Check non-vendor package files ignoring all generated files
echo "Running golint"
for pkg in $mypkgs; do
	mypkgfiles=$(find ${pkg##*dws-operator/} -maxdepth 1 -type f \( ! -iname "*zz_generated*" \))
	echo "Checking:"
	echo "$mypkgfiles"
	golint -min_confidence 0.8 -set_exit_status $mypkgfiles
	if [ $? -ne 0 ] ; then
		echo "failed"
		exit 1
	fi
done

echo "success"
exit 0