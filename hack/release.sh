#!/bin/bash

echo ""
echo "This script will create the control-plane-components.yaml file."
echo "Make sure you call it from the root of the project."
echo ""


COMPSFILE=control-plane-components.yaml

make manifests
bin/kustomize build config/crd/ > $COMPSFILE
echo "---" >> $COMPSFILE
bin/kustomize build config/default/ >> $COMPSFILE

